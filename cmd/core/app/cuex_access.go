/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"context"
	"time"

	cuexv1alpha1 "github.com/kubevela/pkg/apis/cue/v1alpha1"
	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/pkg/cue/cuex/access"
	"sigs.k8s.io/controller-runtime/pkg/client"

	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	"github.com/oam-dev/kubevela/pkg/workflow/providers"
)

// installCallCheck gives every compiler vela-core renders with the check on
// calls to restricted Packages, answered through cli: the manager's client, so
// bindings, namespaces and Packages are read from its cache. A grant is asked
// again once its answer is older than recheck.
func installCallCheck(cli client.Client, recheck time.Duration) {
	checker := &access.Checker{
		Authorizer: &access.KubeAuthorizer{Reader: cli, Reviewer: cli, TTL: recheck},
		Packages: func(namespace, name string) (*cuexv1alpha1.Package, bool) {
			p := &cuexv1alpha1.Package{}
			if err := cli.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, p); err != nil {
				return nil, false
			}
			return p, true
		},
	}
	for _, c := range []*cuex.Compiler{
		velacuex.WorkloadCompiler.Get(),
		velacuex.SourceCompiler.Get(),
		velacuex.ConfigCompiler.Get(),
		providers.DefaultCompiler.Get(),
		cuex.DefaultCompiler.Get(),
	} {
		c.CallCheck = checker.Check
	}
}
