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

package main

import (
	"github.com/coreos/mantle/cli"

	"github.com/coreos/mantle/Godeps/_workspace/src/github.com/coreos/pkg/capnslog"
	"github.com/coreos/mantle/Godeps/_workspace/src/github.com/spf13/cobra"
)

var plog = capnslog.NewPackageLogger("github.com/coreos/mantle", "arne")
var root = &cobra.Command{
	Use:   "arne [command]",
	Short: "The CoreOS SDK Manager",
	// Arne Saknussemm discovered a passage to the center of the Earth.
	// https://en.wikipedia.org/wiki/Journey_to_the_Center_of_the_Earth
}

func main() {
	cli.Execute(root)
}