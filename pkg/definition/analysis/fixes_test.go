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

// fix applies the fix titled title, of the diagnostic whose message holds
// msg, to src.
func fix(t *testing.T, src, msg, title string, opts ...Options) string {
	t.Helper()
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	var titles []string
	for _, d := range AnalyzeWith("def.cue", []byte(src), o).Diagnostics {
		if !strings.Contains(d.Message, msg) {
			continue
		}
		for _, f := range d.Fixes {
			titles = append(titles, f.Title)
			if f.Title == title {
				return applyRangeEdits(src, f.Edits)
			}
		}
	}
	require.Failf(t, "no such fix", "%q for %q among %v", title, msg, titles)
	return ""
}

const fixBase = `import (
	"strings"
	"encoding/json"
)

"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		metadata: name: strings.ToLower(context.name)
		data: image: parameter.image
	}
	parameter: {
		// +usage=The image to run
		image: string
		tag: string
	}
}
`

func TestQuickFixes(t *testing.T) {
	t.Run("a misspelt marker", func(t *testing.T) {
		src := strings.Replace(fixBase, "+usage=The image", "+usge=The image", 1)
		assert.Contains(t, fix(t, src, "unknown marker +usge", "Change to +usage"), "// +usage=The image to run")
	})
	t.Run("an unused import", func(t *testing.T) {
		got := fix(t, fixBase, `imported and not used: "encoding/json"`, "Remove the import")
		assert.NotContains(t, got, "encoding/json")
		assert.Contains(t, got, "import (\n\t\"strings\"\n)")
	})
	t.Run("the only import, unused", func(t *testing.T) {
		src := strings.Replace(fixBase, "import (\n\t\"strings\"\n\t\"encoding/json\"\n)", `import "encoding/json"`, 1)
		src = strings.Replace(src, "strings.ToLower(context.name)", "context.name", 1)
		got := fix(t, src, "imported and not used", "Remove the import")
		assert.True(t, strings.HasPrefix(got, "\n\"web\": {"), got)
	})
	t.Run("a parameter without +usage", func(t *testing.T) {
		got := fix(t, fixBase, "tag has no +usage", "Add +usage")
		assert.Contains(t, got, "\t\t// +usage=\n\t\ttag: string")
	})
	t.Run("a misspelt parameter read", func(t *testing.T) {
		src := strings.Replace(fixBase, "parameter.image", "parameter.imge", 1)
		assert.Contains(t, fix(t, src, "parameter has no field imge", "Change to image"), "data: image: parameter.image")
		got := fix(t, src, "parameter has no field imge", "Add imge to parameter")
		assert.Contains(t, got, "\t\ttag: string\n\t\timge: _\n\t}")
	})
	t.Run("a parameter added to an empty one", func(t *testing.T) {
		src := strings.Replace(fixBase, "parameter: {\n\t\t// +usage=The image to run\n\t\timage: string\n\t\ttag: string\n\t}", "parameter: {}", 1)
		got := fix(t, src, "parameter has no field image", "Add image to parameter")
		assert.Contains(t, got, "parameter: {\n\t\timage: _\n\t}")
	})
	t.Run("a misspelt context field", func(t *testing.T) {
		src := strings.Replace(fixBase, "context.name", "context.nmae", 1)
		assert.Contains(t, fix(t, src, "nmae", "Change to name"), "strings.ToLower(context.name)")
	})
}

func TestQuickFixForAParentsParameter(t *testing.T) {
	parent := `"base": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
	parameter: {
		// +usage=The image
		image: string
	}
}
`
	child := "\"child\": {\n\ttype: \"component\"\n\textends: \"base\"\n}\ntemplate: {\n\t$super: properties: imge: \"nginx\"\n}\n"
	opts := Options{Definitions: func(name string) (string, []byte, bool) { return "base.cue", []byte(parent), name == "base" }}
	assert.Contains(t, fix(t, child, "base takes no parameter imge", "Change to image", opts), `$super: properties: image: "nginx"`)
}
