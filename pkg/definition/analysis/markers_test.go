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

// stepHeader is a header as long as componentHeader for a type whose template
// needs no particular field, so a case is about its markers alone.
const stepHeader = `"my-step": {
	type:        "workflow-step"
	description: "A step"
}
`

func TestMarkers(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []want
	}{
		"parameter markers in place": {
			src: stepHeader + `template: {
	output: spec: replicas: parameter.replicas
	parameter: {
		// +usage=How many replicas to run
		// +short=r
		// +alias=count
		// +immutable
		// +ui:type=Number
		// +ui:colSpan=6
		// +ui:order=2
		// +ui:advanced
		// +ui:hidden=true
		// +ui:optionsFrom=configs:image-registry
		// +ui:suggest=1, 2, 3
		// +ui:format=table
		// +ui:expression=never
		replicas: *1 | int
		// +ignore
		internal?: string
	}
}
`,
		},
		"misspelt parameter marker": {
			src: stepHeader + `template: {
	parameter: {
		// +usge=How many replicas to run
		replicas: *1 | int
	}
}
`,
			want: []want{{7, `unknown marker +usge: did you mean +usage?`}},
		},
		"misspelt ui key": {
			src: stepHeader + `template: {
	parameter: {
		// +ui:colspan=6
		replicas: *1 | int
	}
}
`,
			want: []want{{7, `unknown marker +ui:colspan: did you mean +ui:colSpan?`}},
		},
		"bad ui values": {
			src: stepHeader + `template: {
	parameter: {
		// +ui:colSpan=wide
		// +ui:optionsFrom=secrets
		// +ui:format=grid
		// +ui:expression=always
		// +ui:advanced=yes
		replicas: *1 | int
	}
}
`,
			want: []want{
				{7, "+ui:colSpan takes a whole number"},
				{8, "+ui:optionsFrom takes configs:<template>, clusters or envs"},
				{9, "+ui:format takes table"},
				{10, "+ui:expression takes never"},
				{11, "+ui:advanced takes no value, or true"},
			},
		},
		"marker missing its value, or given one it does not take": {
			src: stepHeader + `template: {
	parameter: {
		// +usage
		// +immutable=true
		// +short=rr
		replicas: *1 | int
	}
}
`,
			want: []want{
				{7, "+usage takes a value: +usage=..."},
				{8, "+immutable takes no value"},
				{9, "+short takes one character"},
			},
		},
		"patch markers in place": {
			src: traitHeader + `template: {
	patch: spec: template: spec: {
		// +patchKey=name
		containers: [{name: "x"}]
		// +patchStrategy=retainKeys
		volumes: [{name: "v"}]
	}
}
`,
		},
		"patch strategy for the whole patch": {
			src: traitHeader + `template: {
	// +patchStrategy=jsonMergePatch
	patch: metadata: labels: a: "b"
}
`,
		},
		"bad patch strategy": {
			src: traitHeader + `template: {
	patch: spec: template: spec: {
		// +patchStrategy=replcae
		containers: [{name: "x"}]
		// +patchStrategy=open
		volumes: [{name: "v"}]
		// +patchStrategy=jsonPatch
		initContainers: [{name: "i"}]
	}
}
`,
			want: []want{
				{7, "+patchStrategy=replcae is not a strategy: retainKeys, replace, jsonPatch or jsonMergePatch"},
				{9, "+patchStrategy=open is not a strategy"},
				{11, "+patchStrategy=jsonPatch applies to the whole patch: put it above patch"},
			},
		},
		"markers outside parameter and patch, which references carry": {
			src: traitHeader + `template: {
	_container: {
		// +patchStrategy=retainKeys
		image: parameter.image
	}
	// +usage=read through a reference
	#Probe: port: int
	patch: spec: template: spec: containers: [_container]
	parameter: {
		image: string
		probe?: #Probe
	}
}
`,
		},
		"markers in the header": {
			src: `"x": {
	// +usage=not read here
	type: "component"
}
template: output: {apiVersion: "v1", kind: "ConfigMap"}
`,
			want: []want{{2, "markers have no effect in the definition's header"}},
		},
		"misspelt marker outside parameter": {
			src: traitHeader + `template: {
	_c: {
		// +patchStratgy=replace
		args: [...string]
	}
	patch: spec: template: spec: containers: [_c]
}
`,
			want: []want{{7, "unknown marker +patchStratgy: did you mean +patchStrategy?"}},
		},
		"words that only look like markers": {
			src: stepHeader + `template: {
	// +kubebuilder:validation:Optional
	// +optional
	output: {}
}
`,
		},
		"comments that are not markers": {
			src: stepHeader + `template: {
	// a + b is fine
	// +1 to this idea
	parameter: {
		// +usage=Use a+b=c freely
		x: string
	}
}
`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := Analyze("def.cue", []byte(tc.src))
			res.Diagnostics = withoutInfo(res.Diagnostics)
			var warnings []Diagnostic
			for _, d := range res.Diagnostics {
				require.Equal(t, SeverityWarning, d.Severity, "only warnings expected, got %d: %s", d.Range.Start.Line, d.Message)
				warnings = append(warnings, d)
			}
			got := lines(warnings)
			require.Len(t, warnings, len(tc.want), "diagnostics: %s", strings.Join(got, "\n"))
			for i, w := range tc.want {
				assert.Equal(t, w.line, warnings[i].Range.Start.Line, got[i])
				assert.Contains(t, warnings[i].Message, w.msg, got[i])
			}
		})
	}
}

// A marker's range is the marker itself, not the whole comment.
func TestMarkerRange(t *testing.T) {
	src := stepHeader + "template: parameter: {\n\t// +usge=x\n\ta: string\n}\n"
	res := Analyze("def.cue", []byte(src))
	res.Diagnostics = withoutInfo(res.Diagnostics)
	require.Len(t, res.Diagnostics, 1)
	assert.Equal(t, Range{Start: Position{Line: 6, Column: 5}, End: Position{Line: 6, Column: 10}}, res.Diagnostics[0].Range)
}
