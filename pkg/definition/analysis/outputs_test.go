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

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

func builtinKinds(t *testing.T) Options {
	t.Helper()
	kinds, err := kubeschema.Builtin()
	require.NoError(t, err)
	return Options{Kinds: kinds}
}

func TestOutputsAgainstTheirKinds(t *testing.T) {
	opts := builtinKinds(t)
	cases := map[string]struct {
		src  string
		want []want
	}{
		"a valid deployment and service": {
			src: componentHeader + `template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: {
			replicas: parameter.replicas
			selector: matchLabels: app: context.name
			template: spec: containers: [{name: context.name, image: parameter.image, imagePullPolicy: "Always"}]
		}
	}
	outputs: svc: {
		apiVersion: "v1"
		kind:       "Service"
		spec: ports: [{port: 80, targetPort: "http"}]
	}
	parameter: {
		replicas: *1 | int
		image:    string
	}
}
`,
		},
		"misspelt field in output": {
			src: componentHeader + `template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: replicass: 2
	}
}
`,
			want: []want{{9, "output.spec.replicass: field not allowed (apps/v1 Deployment)"}},
		},
		"wrong type in an outputs entry": {
			src: componentHeader + `template: {
	output: {apiVersion: "v1", kind: "ConfigMap", data: a: "b"}
	outputs: svc: {
		apiVersion: "v1"
		kind:       "Service"
		spec: ports: [{port: "80"}]
	}
}
`,
			want: []want{{10, "outputs.svc.spec.ports.0.port"}},
		},
		"a value not in the kind's enum": {
			src: componentHeader + `template: output: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	spec: template: spec: containers: [{name: "c", imagePullPolicy: "Sometimes"}]
}
`,
			want: []want{{8, "imagePullPolicy"}},
		},
		"a kind with no schema is left alone": {
			src: componentHeader + `template: output: {
	apiVersion: "example.com/v1"
	kind:       "Widget"
	spec: anything: 1
}
`,
		},
		"a kind decided by a parameter is left alone": {
			src: componentHeader + `template: {
	output: {
		apiVersion: "apps/v1"
		kind:       parameter.kind
		spec: replicass: 2
	}
	parameter: kind: string
}
`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := AnalyzeWith("def.cue", []byte(tc.src), opts)
			got := lines(res.Diagnostics)
			require.Len(t, res.Diagnostics, len(tc.want), "diagnostics: %s", strings.Join(got, "\n"))
			for i, w := range tc.want {
				assert.Equal(t, w.line, res.Diagnostics[i].Range.Start.Line, got[i])
				assert.Contains(t, res.Diagnostics[i].Message, w.msg, got[i])
			}
		})
	}
}

// Without kinds, outputs are not checked against them.
func TestOutputsUncheckedWithoutKinds(t *testing.T) {
	src := componentHeader + "template: output: {apiVersion: \"apps/v1\", kind: \"Deployment\", spec: replicass: 2}\n"
	assert.Empty(t, Analyze("def.cue", []byte(src)).Diagnostics)
}
