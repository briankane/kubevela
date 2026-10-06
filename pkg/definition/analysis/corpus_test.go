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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The definitions KubeVela ships must analyse clean, with their outputs checked
// against Kubernetes' own kinds: an error on any of them is a false positive,
// most likely a context key missing from context.go. A warning must be one of
// knownWarnings. Recommendations are not judged.
func TestShippedDefinitionsAreClean(t *testing.T) {
	root := filepath.Join("..", "..", "..", "vela-templates", "definitions")
	n := checkCorpus(t, root)
	require.NotZero(t, n, "no definitions found under %s", root)
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

// checkCorpus analyses every definition under root and returns how many it
// checked.
// knownWarnings are the warnings shipped definitions are known to have, by
// path under the corpus root.
var knownWarnings = map[string][]string{
	// defkit emits +patchStrategy=open, which no patcher reads; vela-go-definitions
	// generates the same command trait.
	"internal/trait/command.cue": {"72: +patchStrategy=open is not a strategy: retainKeys, replace, jsonPatch or jsonMergePatch, so it has no effect"},
	"trait/command.cue":          {"72: +patchStrategy=open is not a strategy: retainKeys, replace, jsonPatch or jsonMergePatch, so it has no effect"},
	// vela-go-definitions generates these steps with an import they never use.
	"workflowstep/apply-terraform-provider.cue": {`5: imported and not used: "strings"`},
	"workflowstep/build-push-image.cue":         {`5: imported and not used: "encoding/json"`},
}

// knownBroken are definitions known not to compile, by path under the corpus
// root: vela-go-definitions generates these workflow steps with a field read
// from an `if` block other than the one that declares it, which CUE rejects.
var knownBroken = map[string]bool{
	"workflowstep/collect-service-endpoints.cue": true,
	"workflowstep/depends-on-app.cue":            true,
	"workflowstep/export2secret.cue":             true,
	"workflowstep/notification.cue":              true,
	"workflowstep/webhook.cue":                   true,
}

func checkCorpus(t *testing.T, root string) int {
	opts := builtinKinds(t)
	n := 0
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".cue") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		res := AnalyzeWith(path, src, opts)
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if !res.IsDefinition || knownBroken[rel] {
			return nil
		}
		n++
		t.Run(path, func(t *testing.T) {
			var got []string
			for _, d := range res.Diagnostics {
				if d.Severity == SeverityInfo {
					continue
				}
				got = append(got, fmt.Sprintf("%d: %s", d.Range.Start.Line, d.Message))
			}
			assert.Equal(t, knownWarnings[rel], got)
		})
		return nil
	})
	require.NoError(t, err)
	return n
}
