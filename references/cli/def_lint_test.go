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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lintTypo = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "apps/v1", kind: "Deployment", spec: replicass: 2}
	parameter: {}
}
`

const lintClean = `"cm": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "v1", kind: "ConfigMap", metadata: name: context.name}
	parameter: {}
}
`

func runLint(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewDefinitionLintCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDefinitionLint(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web.cue"), []byte(lintTypo), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cm.cue"), []byte(lintClean), 0o600))

	out, err := runLint(t, dir)
	require.Error(t, err, "an error fails the run")
	assert.Contains(t, out, filepath.Join(dir, "web.cue")+":6:")
	assert.Contains(t, out, "error: output.spec.replicass")
	assert.Contains(t, out, "2 files: 1 errors")

	out, err = runLint(t, dir, "--kinds=false")
	require.NoError(t, err, out)
	assert.Contains(t, out, "2 files: 0 errors")

	out, _ = runLint(t, dir, "-q")
	assert.NotContains(t, out, "replicass", "the summary only")

	out, _ = runLint(t, dir, "--json")
	var r struct {
		Files    int `json:"files"`
		Errors   int `json:"errors"`
		Findings []struct {
			Line int `json:"line"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[:bytes.LastIndexByte([]byte(out), '}')+1]), &r))
	assert.Equal(t, 2, r.Files)
	assert.Equal(t, 1, r.Errors)

	require.NoError(t, os.Remove(filepath.Join(dir, "web.cue")))
	_, err = runLint(t, dir, "--fail-on", "nope")
	assert.Error(t, err)
}
