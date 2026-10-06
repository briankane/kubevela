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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The controller fails to render a component with an import it does not use.
func TestUnusedImports(t *testing.T) {
	src := `import (
	"strings"
	"vela/kube"
	j "encoding/json"
)

"c": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "v1", kind: "ConfigMap", metadata: name: strings.ToLower("X")}
	parameter: {}
}
`
	var got []Diagnostic
	for _, d := range Analyze("def.cue", []byte(src)).Diagnostics {
		if d.Severity == SeverityError {
			got = append(got, d)
		}
	}
	require.Len(t, got, 2, "%v", got)
	assert.Equal(t, 3, got[0].Range.Start.Line)
	assert.Equal(t, `imported and not used: "vela/kube"`, got[0].Message)
	assert.Equal(t, 4, got[1].Range.Start.Line)
	assert.Contains(t, got[1].Message, `"encoding/json" as j`)
}

// A workflow step compiles with an unused import, so it only warns.
func TestUnusedImportInAStepWarns(t *testing.T) {
	src := "import \"strings\"\n\n\"s\": {\n\ttype: \"workflow-step\"\n}\ntemplate: {\n\tparameter: {}\n}\n"
	var got []Diagnostic
	for _, d := range Analyze("def.cue", []byte(src)).Diagnostics {
		if d.Message == `imported and not used: "strings"` {
			got = append(got, d)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, SeverityWarning, got[0].Severity)
}
