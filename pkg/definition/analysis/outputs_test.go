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
	"strconv"
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
			res.Diagnostics = withoutInfo(res.Diagnostics)
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

// patchTrait is a trait applying to Deployments and StatefulSets whose patch
// is the given CUE.
func patchTrait(applies, patch string) string {
	return `"my-trait": {
	type: "trait"
	attributes: appliesToWorkloads: [` + applies + `]
}
template: {
	patch: ` + patch + `
	parameter: replicas: *1 | int
}
`
}

func TestTraitPatchesAgainstTheirKinds(t *testing.T) {
	opts := builtinKinds(t)
	errorsIn := func(src string) []string {
		var out []string
		for _, d := range AnalyzeWith("def.cue", []byte(src), opts).Diagnostics {
			if d.Severity == SeverityError {
				out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
			}
		}
		return out
	}
	both := `"deployments.apps", "statefulsets.apps"`

	assert.Empty(t, errorsIn(patchTrait(both, "spec: replicas: parameter.replicas")))
	assert.Empty(t, errorsIn(patchTrait(both, `spec: template: spec: containers: [{name: "x", image: "y"}]`)), "a partial object, list items included")

	got := errorsIn(patchTrait(both, "spec: replicass: parameter.replicas"))
	require.NotEmpty(t, got)
	joined := strings.Join(got, "\n")
	assert.Contains(t, joined, "6: ")
	assert.Contains(t, joined, "replicass")
	assert.Contains(t, joined, "apps/v1 Deployment")

	got = errorsIn(patchTrait(both, `spec: replicas: "three"`))
	assert.Contains(t, strings.Join(got, "\n"), "replicas")

	assert.Contains(t, joined, "apps/v1 StatefulSet", "wrong for every kind, so naming each")

	// A trait may patch each kind differently, choosing by a parameter, so a
	// field one kind has passes.
	assert.Empty(t, errorsIn(patchTrait(both, `spec: serviceName: "x"`)))

	for name, applies := range map[string]string{
		"any workload":    `"*"`,
		"by component":    `"webservice"`,
		"an unknown kind": `"widgets.example.com"`,
	} {
		assert.Empty(t, errorsIn(patchTrait(applies, "spec: replicass: 1")), name)
	}
	jsonPatch := patchTrait(both, `[{op: "add", path: "/spec/replicas", value: 1}]`)
	jsonPatch = strings.Replace(jsonPatch, "\tpatch:", "\t// +patchStrategy=jsonPatch\n\tpatch:", 1)
	assert.Empty(t, errorsIn(jsonPatch), "a JSON patch is a list of operations")
	assert.Empty(t, withoutInfo(AnalyzeWith("def.cue", []byte(patchTrait(both, "spec: replicass: 1")), Options{}).Diagnostics), "no kinds, no check")
}
