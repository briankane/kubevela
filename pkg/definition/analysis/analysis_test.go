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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const componentHeader = `"my-worker": {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
`

const traitHeader = `"my-trait": {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
`

// want is a diagnostic expected on a line, matched on a substring of its message.
type want struct {
	line int
	msg  string
}

func lines(diags []Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%d: %s", d.Range.Start.Line, d.Message))
	}
	return out
}

func TestAnalyzeDiagnostics(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []want
	}{
		"valid component": {
			src: componentHeader + `template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: name: context.name
		spec: replicas: parameter.replicas
	}
	parameter: replicas: *1 | int
}
`,
		},
		"worked example from the plan": {
			src: `import "vela/kube"

` + componentHeader + `template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: name: context.nmae
		spec: replicas: parameter.replicas
	}
	output: kind: "StatefulSet"
	parameter: {
		replicas: *1 | int
		image:    strng
	}
	_x: kube.#Nope
}
`,
			want: []want{
				{10, "conflicting values"},
				{11, "nmae"},
				{14, "conflicting values"},
				{17, "strng"},
				{19, "#Nope"},
			},
		},
		"unknown vela package": {
			src: `import "vela/nope"

` + componentHeader + `template: output: kind: nope.#X
`,
			want: []want{{1, "vela/nope"}},
		},
		"workflow-only package is not available to a component": {
			src: `import "vela/op"

` + componentHeader + `template: output: op.#Apply
`,
			want: []want{{1, "vela/op"}},
		},
		"syntax error": {
			src: componentHeader + `template: {
	output: {
		kind: "Deployment"
}
`,
			want: []want{{8, ""}},
		},
		"optional context field tested for existence": {
			src: componentHeader + `template: {
	output: {
		if context.config != _|_ {
			env: context.config
		}
	}
}
`,
		},
		"trait reads the component's output": {
			src: traitHeader + `template: {
	patch: metadata: labels: app: context.output.metadata.name
	outputs: svc: metadata: name: context.name
}
`,
		},
		"component has no traitType": {
			src: componentHeader + `template: output: metadata: name: context.traitType
`,
			want: []want{{5, "traitType"}},
		},
		"parameter field not declared": {
			src: componentHeader + `template: {
	output: spec: {
		replicas: parameter.replcas
		image:    parameter.nested.b
		labels:   parameter.labels.anything
		extra:    parameter.open.anything
	}
	parameter: {
		replicas: *1 | int
		nested: a: int
		labels: [string]: string
		open: {...}
	}
}
`,
			want: []want{{7, "parameter has no field replcas"}, {8, "parameter.nested has no field b"}},
		},
		"parameter field read inside a list comprehension": {
			src: componentHeader + `template: {
	output: spec: containers: [for c in parameter.containers {
		image: parameter.imagee
	}]
	parameter: {
		image: string
		containers: [...string]
	}
}
`,
			want: []want{{7, "parameter has no field imagee"}},
		},
		"context typo does not hide a parameter typo": {
			src: componentHeader + `template: {
	output: metadata: name: context.nmae
	output: spec: image: parameter.imagee
	parameter: image: string
}
`,
			want: []want{{6, "nmae"}, {7, "parameter has no field imagee"}},
		},
		"field declared and read inside an undecided if": {
			src: componentHeader + `template: {
	output: spec: {
		if parameter.outer != _|_ {
			tmps: [1, 2]
			items: [for x in tmps if parameter.outer {x}]
		}
	}
	parameter: outer?: bool
}
`,
		},
		"misspelt header key": {
			src: `"x": {
	type:       "component"
	descripton: "typo"
}
template: output: {}
`,
			want: []want{{3, "descripton"}},
		},
		"misspelt key beside a value of the wrong type": {
			src: `"x": {
	type:       "trait"
	descripton: "typo"
	attributes: podDisruptive: "yes"
}
template: patch: {}
`,
			want: []want{{3, "descripton"}, {4, "podDisruptive"}},
		},
		"misspelt attributes key": {
			src: `"x": {
	type: "component"
	attributes: workloadd: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: output: {}
`,
			want: []want{{3, "workloadd"}},
		},
		"attribute of the wrong type": {
			src: `"x": {
	type: "trait"
	attributes: podDisruptive: "yes"
}
template: patch: {}
`,
			want: []want{{3, "podDisruptive"}},
		},
		"trait attribute on a component": {
			src: `"x": {
	type: "component"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: output: {}
`,
			want: []want{{3, "appliesToWorkloads"}},
		},
		"workload without a kind": {
			src: `"x": {
	type: "component"
	attributes: workload: definition: apiVersion: "apps/v1"
}
template: output: {}
`,
			want: []want{{3, "kind"}},
		},
		"missing type": {
			src: `"x": {
	attributes: {}
}
template: output: {}
`,
			want: []want{{1, "type"}},
		},
		"unknown type": {
			src: `"x": {
	type: "widget"
}
template: output: {}
`,
			want: []want{{2, "widget"}},
		},
		"customStatus without message": {
			src: `"x": {
	type: "component"
	attributes: status: customStatus: #"""
		phase: "ok"
		"""#
}
template: output: {}
`,
			want: []want{{3, "message"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := Analyze("def.cue", []byte(tc.src))
			require.True(t, res.IsDefinition, "should be recognised as a definition")
			got := lines(res.Diagnostics)
			require.Len(t, res.Diagnostics, len(tc.want), "diagnostics: %s", strings.Join(got, "\n"))
			for i, w := range tc.want {
				d := res.Diagnostics[i]
				assert.Equal(t, w.line, d.Range.Start.Line, "diagnostic %d: %s", i, got[i])
				assert.Contains(t, d.Message, w.msg, "diagnostic %d", i)
			}
		})
	}
}

func TestAnalyzeIgnoresPlainCUE(t *testing.T) {
	for name, src := range map[string]string{
		"plain values":         "a: 1\nb: a + 1\n",
		"template but no type": "template: output: {}\n",
		"broken plain cue":     "a: {\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := Analyze("plain.cue", []byte(src))
			assert.False(t, res.IsDefinition)
			assert.Empty(t, res.Diagnostics)
		})
	}
}

func TestAnalyzeReportsType(t *testing.T) {
	res := Analyze("def.cue", []byte(traitHeader+"template: patch: {}\n"))
	assert.Equal(t, "trait", res.Type)
	assert.Equal(t, "my-trait", res.Name)
}
