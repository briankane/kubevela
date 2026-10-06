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

// wantSev is a diagnostic expected on a line, with its severity.
type wantSev struct {
	line int
	sev  Severity
	msg  string
}

func header(name, typ, extra string) string {
	return "\"" + name + "\": {\n\ttype: \"" + typ + "\"\n" + extra + "}\n"
}

func TestTemplateSchemas(t *testing.T) {
	cases := map[string]struct {
		src  string
		want []wantSev
	}{
		"a component with helper fields": {
			src: header("c", "component", "") + `template: {
	mountsArray: [for m in parameter.mounts {m}]
	_hidden: 1
	#Helper: x: int
	output: {apiVersion: "v1", kind: "ConfigMap"}
	outputs: svc: {apiVersion: "v1", kind: "Service"}
	errs: [if parameter.bad {"bad"}]
	parameter: {mounts: [...{...}], bad: *false | bool}
}
`,
		},
		"schema is optional outside sources": {
			src: header("c", "component", "") + `template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
	schema: replicas: int
}
`,
		},
		"output that agrees with its schema": {
			src: header("c", "component", "") + `template: {
	schema: {replicas: int, image: string}
	output: {apiVersion: "v1", kind: "ConfigMap", replicas: parameter.replicas, image: "nginx"}
	parameter: replicas: *1 | int
}
`,
		},
		"output that disagrees with its schema": {
			src: header("c", "component", "") + `template: {
	schema: replicas: int
	output: {apiVersion: "v1", kind: "ConfigMap", replicas: "two"}
}
`,
			want: []wantSev{
				{5, SeverityError, "output.replicas: conflicting values"},
				{6, SeverityError, "output.replicas: conflicting values"},
			},
		},
		"a source's output must match its schema": {
			src: header("s", "source", "") + `template: {
	schema: {region: string, zones: [...string]}
	output: {region: "eu-west-1", zones: "a"}
}
`,
			want: []wantSev{
				{5, SeverityError, "output.zones: conflicting values"},
				{6, SeverityError, "output.zones: conflicting values"},
			},
		},
		"an output that is not a Kubernetes object": {
			src: header("c", "component", "") + `template: {
	output: {}
	outputs: {
		svc: {apiVersion: "v1"}
		cm: {apiVersion: "v1", kind: "ConfigMap"}
	}
}
`,
			want: []wantSev{
				{5, SeverityError, "output is not a Kubernetes object: it has no apiVersion or kind"},
				{7, SeverityError, "outputs.svc is not a Kubernetes object: it has no kind"},
			},
		},
		"objects whose kind comes from a parameter": {
			src: header("c", "component", "") + `template: {
	output: {apiVersion: parameter.apiVersion, kind: parameter.kind}
	parameter: {apiVersion: string, kind: string}
}
`,
		},
		"a trait's outputs are objects too": {
			src: header("t", "trait", "") + `template: {
	outputs: ingress: spec: rules: []
}
`,
			want: []wantSev{{5, SeverityError, "outputs.ingress is not a Kubernetes object: it has no apiVersion or kind"}},
		},
		"schema of the wrong shape": {
			src: header("t", "trait", "") + `template: {
	patch: {}
	schema: "replicas"
}
`,
			want: []wantSev{{6, SeverityError, "schema"}},
		},
		"errs of the wrong shape": {
			src: header("c", "component", "") + `template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
	errs: "boom"
}
`,
			want: []wantSev{{6, SeverityError, "errs"}},
		},
		"a component without output": {
			src: header("c", "component", "") + `template: {
	outputs: svc: {apiVersion: "v1", kind: "Service"}
}
`,
			want: []wantSev{{4, SeverityError, "a component's template must declare output"}},
		},
		"output declared under a condition": {
			src: header("c", "component", "") + `template: {
	if parameter.on {
		output: {apiVersion: "v1", kind: "ConfigMap"}
	}
	parameter: on: bool
}
`,
		},
		"a component that extends another inherits its output": {
			src: header("c", "component", "\textends: \"webservice\"\n") + `template: {
	$super: properties: image: "nginx"
}
`,
		},
		"fields a component's template never reads": {
			src: header("c", "component", "") + `template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
	patch: metadata: labels: a: "b"
}
`,
			want: []wantSev{{6, SeverityWarning, "patch has no effect in a component's template"}},
		},
		"a trait's patchOutputs and inherit of the wrong shape": {
			src: header("t", "trait", "") + `template: {
	patchOutputs: "svc"
	$inherit: "yes"
}
`,
			want: []wantSev{{5, SeverityError, "patchOutputs"}, {6, SeverityError, "$inherit"}},
		},
		"output in a trait": {
			src: header("t", "trait", "") + `template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
}
`,
			want: []wantSev{{5, SeverityWarning, "output has no effect in a trait's template"}},
		},
		"an application-scoped policy's output is closed": {
			src: header("p", "policy", "\tattributes: scope: \"Application\"\n") + `template: {
	output: {
		labels: team: "a"
		componentz: []
	}
}
`,
			want: []wantSev{{8, SeverityError, "componentz"}},
		},
		"a source needs schema and output": {
			src: header("s", "source", "") + `template: {
	parameter: name: string
}
`,
			want: []wantSev{{4, SeverityError, "a source's template must declare schema and output"}},
		},
		"a source's storage": {
			src: header("s", "source", "") + `template: {
	schema: value: string
	output: value: "x"
	storage: {
		onStaleFailure: "maybe"
		ttl: "1h"
	}
}
`,
			want: []wantSev{{8, SeverityError, "onStaleFailure"}, {9, SeverityError, "ttl"}},
		},
		"a workflow step is open beyond parameter": {
			src: header("w", "workflow-step", "") + `template: {
	apply: {anything: 1}
	output: "not a resource"
	parameter: "wrong"
}
`,
			want: []wantSev{{7, SeverityError, "parameter"}},
		},
		"inheritance outside components and traits": {
			src: header("w", "workflow-step", "") + `template: {
	$super: properties: {}
}
`,
			want: []wantSev{{5, SeverityWarning, "$super has no effect in a workflow step's template"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := Analyze("def.cue", []byte(tc.src))
			res.Diagnostics = withoutInfo(res.Diagnostics)
			got := lines(res.Diagnostics)
			require.Len(t, res.Diagnostics, len(tc.want), "diagnostics: %s", strings.Join(got, "\n"))
			for i, w := range tc.want {
				d := res.Diagnostics[i]
				assert.Equal(t, w.line, d.Range.Start.Line, got[i])
				assert.Equal(t, w.sev, d.Severity, got[i])
				assert.Contains(t, d.Message, w.msg, got[i])
			}
		})
	}
}
