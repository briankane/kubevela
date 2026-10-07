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
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// A file or folder checked alone is checked against its workspace's index:
// the parent and package in other folders are found, and only what was asked
// for is checked and counted.
func TestCheckPathsInAWorkspace(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o600))
	}
	write("parents/web.cue", parentSrc)
	write("defs/tenant-web.cue", childSrc)
	write("packages/hello-package.yaml", helloPackageYAML)
	write("defs/greeter.cue", usesHelloSrc)
	write("other/typo.cue", strings.Replace(deploymentTypo, `"web": {`, `"typo": {`, 1))

	for name, roots := range map[string][]string{
		"a file":   {filepath.Join(dir, "defs/greeter.cue"), filepath.Join(dir, "defs/tenant-web.cue")},
		"a folder": {filepath.Join(dir, "defs")},
	} {
		findings, files, err := Check(roots, CheckOptions{Kinds: true, Workspace: []string{dir}})
		require.NoError(t, err, name)
		assert.Equal(t, 2, files, "%s: only what was asked for is checked", name)
		var all []string
		for _, f := range findings {
			rel, _ := filepath.Rel(dir, f.Path)
			all = append(all, rel+":"+strconv.Itoa(f.Line)+": "+f.Severity+": "+f.Message)
		}
		joined := strings.Join(all, "\n")
		assert.NotContains(t, joined, "greeter.cue:1: error", "%s: the package is found in the workspace", name)
		assert.Contains(t, joined, "web takes no parameter imge", "%s: the parent is found in the workspace", name)
		assert.NotContains(t, joined, "other/", "%s: what was not asked for is not checked", name)
	}

	findings, _, err := Check([]string{filepath.Join(dir, "defs/greeter.cue")}, CheckOptions{})
	require.NoError(t, err)
	var undefined bool
	for _, f := range findings {
		undefined = undefined || strings.Contains(f.Message, "undefined")
	}
	assert.True(t, undefined, "with no workspace, the file is its own")
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

// The workspace's files are checked once indexed, open or not.
func TestWorkspaceDiagnostics(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "typo.cue")
	require.NoError(t, os.WriteFile(broken, []byte(strings.Replace(deploymentTypo, `"web": {`, `"typo": {`, 1)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web.cue"), []byte(parentSrc), 0o600))

	start := func(options map[string]interface{}) *client {
		c := newClientWith(t, NewServer(WithCluster(noCluster)))
		c.drain()
		options["validateOutputs"] = "on"
		c.response(c.send("initialize", map[string]interface{}{"rootUri": "file://" + dir, "initializationOptions": options}, true))
		c.send("initialized", map[string]interface{}{}, false)
		return c
	}
	published := func(c *client) map[string][]Diagnostic {
		got := map[string][]Diagnostic{}
		for {
			m, ok := c.tryRead(3 * time.Second)
			if !ok {
				return got
			}
			var p PublishDiagnosticsParams
			if string(m["method"]) == `"textDocument/publishDiagnostics"` && json.Unmarshal(m["params"], &p) == nil {
				got[p.URI] = p.Diagnostics
			}
		}
	}

	got := published(start(map[string]interface{}{}))
	require.Contains(t, got, "file://"+broken, "an unopened file is checked")
	assert.Contains(t, messages(got["file://"+broken]), "replicass")
	assert.Contains(t, got, "file://"+filepath.Join(dir, "web.cue"), "a clean file is published too, empty")

	got = published(start(map[string]interface{}{"workspaceDiagnostics": false}))
	assert.NotContains(t, got, "file://"+broken, "off: only open files")
}
