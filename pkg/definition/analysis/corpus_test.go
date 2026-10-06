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

package analysis

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The definitions KubeVela ships must analyse clean: a diagnostic on any of
// them is a false positive, most likely a context key missing from context.go.
func TestShippedDefinitionsAreClean(t *testing.T) {
	root := filepath.Join("..", "..", "..", "vela-templates", "definitions")
	n := checkCorpus(t, root)
	require.NotZero(t, n, "no component or trait definitions found under %s", root)
}

// VELA_ANALYSIS_CORPUS names more directories of definitions to check, such
// as the generated CUE in vela-go-definitions, separated by the path list
// separator.
func TestExtraCorpus(t *testing.T) {
	dirs := os.Getenv("VELA_ANALYSIS_CORPUS")
	if dirs == "" {
		t.Skip("VELA_ANALYSIS_CORPUS not set")
	}
	for _, dir := range filepath.SplitList(dirs) {
		checkCorpus(t, dir)
	}
}

// checkCorpus analyses every component and trait definition under root and
// returns how many it checked.
func checkCorpus(t *testing.T, root string) int {
	n := 0
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".cue") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		res := Analyze(path, src)
		if res.Type != componentType && res.Type != traitType {
			return nil
		}
		n++
		t.Run(path, func(t *testing.T) {
			assert.Empty(t, lines(res.Diagnostics))
		})
		return nil
	})
	require.NoError(t, err)
	return n
}
