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

package lsp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check finds what the editor finds, with the same workspace index: here a
// child checked against its parent in another file, a custom provider's
// package, and an output against its kind.
func TestCheckWorkspace(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o600))
	}
	write("defs/web.cue", parentSrc)
	write("defs/tenant-web.cue", childSrc)
	write("packages/hello-package.yaml", helloPackageYAML)
	write("defs/greeter.cue", usesHelloSrc)
	write("defs/typo.cue", strings.Replace(deploymentTypo, `"web": {`, `"typo": {`, 1))
	write("notes.txt", "not ours")

	findings, files, err := Check([]string{dir}, CheckOptions{Kinds: true})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, files, 5)
	var lines []string
	for _, f := range findings {
		rel, _ := filepath.Rel(dir, f.Path)
		lines = append(lines, rel+":"+strconv.Itoa(f.Line)+": "+f.Severity+": "+f.Message)
	}
	all := strings.Join(lines, "\n")
	assert.Contains(t, all, "defs/tenant-web.cue:6: error: ", "the parent is found in the workspace")
	assert.Contains(t, all, "web takes no parameter imge")
	assert.Contains(t, all, "defs/typo.cue:8: error: output.spec.replicass")
	assert.NotContains(t, all, "greeter.cue:1: error", "the package is found in the workspace")

	findings, _, err = Check([]string{dir}, CheckOptions{})
	require.NoError(t, err)
	for _, f := range findings {
		assert.NotContains(t, f.Message, "replicass", "kinds are checked only when asked")
	}
}

// Definition names are unique: two of one name and type replace each other
// when applied, an error on each; one of another type shares the name, a
// warning. What extends the name finds the first, by path, every time.
func TestDefinitionNamesAreUnique(t *testing.T) {
	dir := t.TempDir()
	trait := "\"web\": {\n\ttype: \"trait\"\n}\ntemplate: patch: metadata: labels: a: \"b\"\n"
	for rel, text := range map[string]string{"a/web.cue": parentSrc, "b/web.cue": deploymentTypo, "tenant-web.cue": childSrc, "traits/web.cue": trait} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o600))
	}
	for i := 0; i < 5; i++ {
		findings, _, err := Check([]string{dir}, CheckOptions{})
		require.NoError(t, err)
		bySeverity := map[string][]string{}
		imge := 0
		for _, f := range findings {
			rel, _ := filepath.Rel(dir, f.Path)
			switch {
			case strings.Contains(f.Message, "is also defined in"), strings.Contains(f.Message, "is also the name of"):
				assert.Equal(t, 1, f.Line, f.Message)
				bySeverity[f.Severity] = append(bySeverity[f.Severity], rel)
			case strings.Contains(f.Message, "web takes no parameter imge"):
				imge++
			}
		}
		assert.ElementsMatch(t, []string{"a/web.cue", "b/web.cue"}, bySeverity[FindingError], "the two components replace each other")
		assert.ElementsMatch(t, []string{"a/web.cue", "b/web.cue", "traits/web.cue"}, bySeverity[FindingWarning], "the trait shares the name")
		assert.Equal(t, 1, imge, "the parent is a/web.cue, every time")
	}
}
