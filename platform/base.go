// Copyright 2015 CoreOS, Inc.
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

package platform

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/pkg/multierror"
	"github.com/satori/go.uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/coreos/mantle/network"
	"github.com/coreos/mantle/platform/conf"
	"github.com/coreos/mantle/util"
)

type BaseCluster struct {
	agent *network.SSHAgent

	machlock sync.Mutex
	machmap  map[string]Machine

	name string
	conf *RuntimeConfig
}

func NewBaseCluster(basename string, conf *RuntimeConfig) (*BaseCluster, error) {
	return NewBaseClusterWithDialer(basename, conf, network.NewRetryDialer())
}

func NewBaseClusterWithDialer(basename string, conf *RuntimeConfig, dialer network.Dialer) (*BaseCluster, error) {
	agent, err := network.NewSSHAgent(dialer)
	if err != nil {
		return nil, err
	}

	bc := &BaseCluster{
		agent:   agent,
		machmap: make(map[string]Machine),
		name:    fmt.Sprintf("%s-%s", basename, uuid.NewV4()),
		conf:    conf,
	}

	return bc, nil
}

func (bc *BaseCluster) SSHClient(ip string) (*ssh.Client, error) {
	sshClient, err := bc.agent.NewClient(ip)
	if err != nil {
		return nil, err
	}

	return sshClient, nil
}

func (bc *BaseCluster) PasswordSSHClient(ip string, user string, password string) (*ssh.Client, error) {
	sshClient, err := bc.agent.NewPasswordClient(ip, user, password)
	if err != nil {
		return nil, err
	}

	return sshClient, nil
}

func (bc *BaseCluster) SSH(m Machine, cmd string) ([]byte, error) {
	client, err := bc.SSHClient(m.IP())
	if err != nil {
		return nil, err
	}

	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	defer session.Close()

	session.Stderr = os.Stderr
	out, err := session.Output(cmd)
	out = bytes.TrimSpace(out)
	return out, err
}

func (bc *BaseCluster) Machines() []Machine {
	bc.machlock.Lock()
	defer bc.machlock.Unlock()
	machs := make([]Machine, 0, len(bc.machmap))
	for _, m := range bc.machmap {
		machs = append(machs, m)
	}
	return machs
}

func (bc *BaseCluster) AddMach(m Machine) {
	bc.machlock.Lock()
	defer bc.machlock.Unlock()
	bc.machmap[m.ID()] = m
}

func (bc *BaseCluster) DelMach(m Machine) {
	bc.machlock.Lock()
	defer bc.machlock.Unlock()
	delete(bc.machmap, m.ID())
}

func (bc *BaseCluster) Keys() ([]*agent.Key, error) {
	return bc.agent.List()
}

func (bc *BaseCluster) MangleUserData(userdata string, ignitionVars map[string]string) (*conf.Conf, error) {
	// hacky solution for unified ignition metadata variables
	if strings.Contains(userdata, `"ignition":`) {
		for k, v := range ignitionVars {
			userdata = strings.Replace(userdata, k, v, -1)
		}
	}

	conf, err := conf.New(userdata)
	if err != nil {
		return nil, err
	}

	if !bc.conf.NoSSHKeyInUserData {
		keys, err := bc.Keys()
		if err != nil {
			return nil, err
		}

		conf.CopyKeys(keys)
	}

	return conf, nil
}

// Destroy destroys each machine in the cluster and closes the SSH agent.
func (bc *BaseCluster) Destroy() error {
	var err multierror.Error

	for _, m := range bc.Machines() {
		if e := m.Destroy(); e != nil {
			err = append(err, e)
		}
	}

	if e := bc.agent.Close(); e != nil {
		err = append(err, e)
	}

	return err.AsError()
}

// XXX(mischief): i don't really think this belongs here, but it completes the
// interface we've established.
func (bc *BaseCluster) GetDiscoveryURL(size int) (string, error) {
	var result string
	err := util.Retry(3, 5*time.Second, func() error {
		resp, err := http.Get(fmt.Sprintf("https://discovery.etcd.io/new?size=%d", size))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("Discovery service returned %q", resp.Status)
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		result = string(body)
		return nil
	})
	return result, err
}

func (bc *BaseCluster) Name() string {
	return bc.name
}

func (bc *BaseCluster) OutputDir() string {
	return bc.conf.OutputDir
}
