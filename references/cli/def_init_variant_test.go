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

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// Every definition vela def init scaffolds analyses without an error, so a
// new definition starts from something that can be applied.
func TestDefinitionInitScaffoldsAnalyseClean(t *testing.T) {
	for _, tc := range []struct{ typ, variant, has string }{
		{"component", "", "output:"},
		{"trait", "", "patch:"},
		{"trait", "patch", "patch:"},
		{"trait", "outputs", "outputs:"},
		{"policy", "", "output:"},
		{"policy", "standard", "output:"},
		{"policy", "application", `scope: "Application"`},
		{"workflow-step", "", "template:"},
		{"source", "", "schema:"},
		{"workload", "", "template:"},
	} {
		t.Run(tc.typ+"/"+tc.variant, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "my-def.cue")
			args := []string{"my-def", "-t", tc.typ, "-o", out}
			if tc.variant != "" {
				args = append(args, "--variant", tc.variant)
			}
			cmd := NewDefinitionInitCommand(initArgs())
			initCommand(cmd)
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())
			src, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.Contains(t, string(src), tc.has)
			var errs []string
			for _, d := range analysis.Analyze(out, src).Diagnostics {
				if d.Severity == analysis.SeverityError {
					errs = append(errs, d.Message)
				}
			}
			assert.Empty(t, errs, "%s", src)
		})
	}
}

func TestDefinitionInitRefusesAVariantTheTypeHasNot(t *testing.T) {
	for _, args := range [][]string{
		{"x", "-t", "trait", "--variant", "standard"},
		{"x", "-t", "component", "--variant", "outputs"},
	} {
		cmd := NewDefinitionInitCommand(initArgs())
		initCommand(cmd)
		cmd.SetArgs(append(args, "-o", filepath.Join(t.TempDir(), "x.cue")))
		err := cmd.Execute()
		require.Error(t, err, strings.Join(args, " "))
		assert.Contains(t, err.Error(), "variant")
	}
}
