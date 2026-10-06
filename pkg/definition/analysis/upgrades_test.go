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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const needsUpgrades = `"c": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_base: ["a"]
	_more: _base + ["b"]
	output: {
		apiVersion: "v1"
		kind: "ConfigMap"
		data: {for i, v in _more {"k\(i)": v}}
	}
	status: error: "x"
	parameter: {}
}
`

func upgradeWarnings(src string) []Diagnostic {
	var out []Diagnostic
	for _, d := range Analyze("def.cue", []byte(src)).Diagnostics {
		if strings.Contains(d.Message, "CUE upgrader") {
			out = append(out, d)
		}
	}
	return out
}

func TestUpgradePatterns(t *testing.T) {
	got := upgradeWarnings(needsUpgrades)
	require.Len(t, got, 2, "%v", got)

	assert.Equal(t, 7, got[0].Range.Start.Line)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Contains(t, got[0].Message, "list-arithmetic")
	assert.Contains(t, got[0].Message, `list.Concat([_base, ["b"]])`)

	assert.Equal(t, 13, got[1].Range.Start.Line)
	assert.Contains(t, got[1].Message, "error-field-label")
	assert.Contains(t, got[1].Message, `"error"`)

	assert.Empty(t, upgradeWarnings(strings.NewReplacer(`_base + ["b"]`, `list.Concat([_base, ["b"]])`, "status: error:", `status: "error":`).Replace("import \"list\"\n\n"+needsUpgrades)))
}

// The fix makes only the upgrade's own changes: the rest of the file keeps
// its formatting, and the import the rewrite needs is added.
func TestUpgradeEdits(t *testing.T) {
	edits := UpgradeEdits(needsUpgrades)
	require.NotEmpty(t, edits)
	fixed := ApplyEdits(needsUpgrades, edits)
	assert.Contains(t, fixed, `import "list"`)
	assert.Contains(t, fixed, `_more: list.Concat([_base, ["b"]])`)
	assert.Contains(t, fixed, `status: "error": "x"`)
	assert.Contains(t, fixed, "\t\tkind: \"ConfigMap\"\n", "an untouched line keeps its formatting")
	assert.Empty(t, upgradeWarnings(fixed), "the fixed file needs no more")
	assert.Empty(t, UpgradeEdits(fixed))
}
