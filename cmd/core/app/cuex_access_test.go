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
	"testing"
	"time"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	"github.com/oam-dev/kubevela/pkg/utils/common"
	"github.com/oam-dev/kubevela/pkg/workflow/providers"
)

// Every compiler vela-core renders with checks calls to restricted Packages; one
// without the check refuses them.
func TestInstallCallCheck(t *testing.T) {
	installCallCheck(fake.NewClientBuilder().WithScheme(common.Scheme).Build(), time.Minute)
	for name, c := range map[string]*cuex.Compiler{
		"workload":         velacuex.WorkloadCompiler.Get(),
		"source":           velacuex.SourceCompiler.Get(),
		"config":           velacuex.ConfigCompiler.Get(),
		"workflow":         providers.DefaultCompiler.Get(),
		"upstream default": cuex.DefaultCompiler.Get(),
	} {
		require.NotNil(t, c.CallCheck, "the %s compiler has no call check", name)
	}
}
