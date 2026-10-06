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

package addon

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// The CUE an addon starts from has no errors, so it can be enabled as written.
func TestInitCmd_ScaffoldAnalysesClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-addon")
	cmd := InitCmd{AddonName: "my-addon", Path: dir}
	require.NoError(t, cmd.CreateScaffold())
	checked := 0
	require.NoError(t, filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".cue") {
			return err
		}
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, d := range analysis.Analyze(path, src).Diagnostics {
			if d.Severity == analysis.SeverityError {
				t.Errorf("%s:%d: %s", filepath.Base(path), d.Range.Start.Line, d.Message)
			}
		}
		checked++
		return nil
	}))
	assert.NotZero(t, checked)
}
