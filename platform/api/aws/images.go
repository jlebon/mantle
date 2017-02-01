// Copyright 2016 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
)

type CreateImageConfig struct {
	// URL specifies an HTTP url to an aws-compatible streaming vmdk from which
	// to create this image
	URL string
	// FilePath specifies a filepath to an aws-compatible streaming vmdk from
	// which to create this image
	FilePath string
}

type EC2ImageType string

const (
	EC2ImageTypeHVM EC2ImageType = "hvm"
	EC2ImageTypePV  EC2ImageType = "pv"
)

type EC2ImageFormat string

const (
	EC2ImageFormatRaw  EC2ImageFormat = ec2.DiskImageFormatRaw
	EC2ImageFormatVmdk                = ec2.DiskImageFormatVmdk
)

func (e *EC2ImageFormat) Set(s string) error {
	switch s {
	case string(EC2ImageFormatVmdk):
		*e = EC2ImageFormatVmdk
	case string(EC2ImageFormatRaw):
		*e = EC2ImageFormatRaw
	default:
		return fmt.Errorf("invalid ec2 image format: must be raw or vmdk")
	}
	return nil
}

func (e *EC2ImageFormat) String() string {
	return string(*e)
}

func (e *EC2ImageFormat) Type() string {
	return "ec2ImageFormat"
}

var vmImportRole = "vmimport"

type Image struct {
	// Image ID
	AMI string
	// Image type, either pv or hvm
	Type EC2ImageType
	// Image region
	Region string
}

type Snapshot struct {
	SnapshotID string
}

// CreateSnapshot creates an AWS Snapshot
func (a *API) CreateSnapshot(description, sourceURL string, format EC2ImageFormat) (*Snapshot, error) {
	s3url, err := url.Parse(sourceURL)
	if err != nil {
		return nil, err
	}
	if s3url.Scheme != "s3" {
		return nil, fmt.Errorf("source must have a 's3://' scheme, not: '%v://'", s3url.Scheme)
	}
	s3key := strings.TrimPrefix(s3url.Path, "/")

	importRes, err := a.ec2.ImportSnapshot(&ec2.ImportSnapshotInput{
		RoleName:    aws.String(vmImportRole),
		Description: aws.String(description),
		DiskContainer: &ec2.SnapshotDiskContainer{
			// TODO(euank): allow s3 source / local file -> s3 source
			UserBucket: &ec2.UserBucket{
				S3Bucket: aws.String(s3url.Host),
				S3Key:    aws.String(s3key),
			},
			Format: aws.String(string(format)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create import snapshot task: %v", err)
	}

	plog.Infof("created snapshot import task %v", *importRes.ImportTaskId)

	snapshotDone := func(snapshotTaskID string) (bool, string, error) {
		taskRes, err := a.ec2.DescribeImportSnapshotTasks(&ec2.DescribeImportSnapshotTasksInput{
			ImportTaskIds: []*string{aws.String(snapshotTaskID)},
		})
		if err != nil {
			return false, "", err
		}

		details := taskRes.ImportSnapshotTasks[0].SnapshotTaskDetail

		// I dream of AWS specifying this as an enum shape, not string
		switch *details.Status {
		case "completed":
			return true, *details.SnapshotId, nil
		case "pending", "active":
			plog.Debugf("waiting for import task: %v (%v): %v", *details.Status, *details.Progress, *details.StatusMessage)
			return false, "", nil
		case "cancelled", "cancelling":
			return false, "", fmt.Errorf("import task cancelled")
		case "deleted", "deleting":
			errMsg := "unknown error occured importing snapshot"
			if details.StatusMessage != nil {
				errMsg = *details.StatusMessage
			}
			return false, "", fmt.Errorf("could not import snapshot: %v", errMsg)
		default:
			return false, "", fmt.Errorf("unexpected status: %v", *details.Status)
		}
	}

	// TODO(euank): write a waiter for import snapshot
	for {
		done, snapshotID, err := snapshotDone(*importRes.ImportTaskId)
		if err != nil {
			return nil, err
		}
		if done {
			return &Snapshot{
				SnapshotID: snapshotID,
			}, nil
		}
		time.Sleep(20 * time.Second)
	}
}

func (a *API) CreateImportRole(bucket string) error {
	iamc := iam.New(a.session)
	_, err := iamc.GetRole(&iam.GetRoleInput{
		RoleName: &vmImportRole,
	})
	if err != nil {
		if awserr, ok := err.(awserr.Error); ok && awserr.Code() == "NoSuchEntity" {
			// Role does not exist, let's try to create it
			_, err := iamc.CreateRole(&iam.CreateRoleInput{
				RoleName: &vmImportRole,
				AssumeRolePolicyDocument: aws.String(`{
					"Version": "2012-10-17",
					"Statement": [{
						"Effect": "Allow",
						"Condition": {
							"StringEquals": {
								"sts:ExternalId": "vmimport"
							}
						},
						"Action": "sts:AssumeRole",
						"Principal": {
							"Service": "vmie.amazonaws.com"
						}
					}]
				}`),
			})
			if err != nil {
				return fmt.Errorf("coull not create vmimport role: %v", err)
			}
		}
	}

	// by convention, name our policies after the bucket so we can identify
	// whether a regional bucket is covered by a policy without parsing the
	// policy-doc json
	policyName := bucket
	_, err = iamc.GetRolePolicy(&iam.GetRolePolicyInput{
		RoleName:   &vmImportRole,
		PolicyName: &policyName,
	})
	if err != nil {
		if awserr, ok := err.(awserr.Error); ok && awserr.Code() == "NoSuchEntity" {
			// Policy does not exist, let's try to create it
			_, err := iamc.PutRolePolicy(&iam.PutRolePolicyInput{
				RoleName:   &vmImportRole,
				PolicyName: &policyName,
				PolicyDocument: aws.String((`{
	"Version": "2012-10-17",
	"Statement": [{
		"Effect": "Allow",
		"Action": [
			"s3:ListBucket",
			"s3:GetBucketLocation",
			"s3:GetObject"
		],
		"Resource": [
			"arn:aws:s3:::` + bucket + `",
			"arn:aws:s3:::` + bucket + `/*"
		]
	},
	{
		"Effect": "Allow",
		"Action": [
			"ec2:ModifySnapshotAttribute",
			"ec2:CopySnapshot",
			"ec2:RegisterImage",
			"ec2:Describe*"
		],
		"Resource": "*"
	}]
}`)),
			})
			if err != nil {
				return fmt.Errorf("could not create role policy: %v", err)
			}
		} else {
			return err
		}
	}

	return nil
}

func (a *API) CreateHVMImage(snapshotID string, name string, description string) (string, error) {
	res, err := a.ec2.RegisterImage(registerImageParams(snapshotID, name, description, EC2ImageTypeHVM))
	if err != nil {
		return "", fmt.Errorf("error creating hvm AMI: %v", err)
	}
	return *res.ImageId, nil
}

func (a *API) CreatePVImage(snapshotID string, name string, description string) (string, error) {
	res, err := a.ec2.RegisterImage(registerImageParams(snapshotID, name, description, EC2ImageTypePV))
	if err != nil {
		return "", fmt.Errorf("error creating hvm AMI: %v", err)
	}
	return *res.ImageId, nil
}

func registerImageParams(snapshotID, name, description string, imageType EC2ImageType) *ec2.RegisterImageInput {
	return &ec2.RegisterImageInput{
		Name:               aws.String(name),
		Description:        aws.String(description),
		Architecture:       aws.String("x86_64"),
		VirtualizationType: aws.String(string(imageType)),
		RootDeviceName:     aws.String("/dev/xvda"),
		BlockDeviceMappings: []*ec2.BlockDeviceMapping{
			&ec2.BlockDeviceMapping{
				DeviceName: aws.String("/dev/xvda"),
				Ebs: &ec2.EbsBlockDevice{
					SnapshotId:          aws.String(snapshotID),
					DeleteOnTermination: aws.Bool(true),
				},
			},
			&ec2.BlockDeviceMapping{
				DeviceName:  aws.String("/dev/xvdb"),
				VirtualName: aws.String("ephemeral0"),
			},
		},
	}
}
