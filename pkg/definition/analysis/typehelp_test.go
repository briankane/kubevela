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
)

const typeHelpDoc = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: {
			replicas: CURSOR_REPLICAS
			template: spec: {
				restartPolicy: CURSOR_RESTART
				containers: [{name: "web", imagePullPolicy: CURSOR_PULL}]
			}
		}
	}
	parameter: {
		#Probe: {path: string}
		image:    string
		replicas: *1 | int
		probe?:   #Probe
		timeout:  CURSOR_PARAM
	}
}
`

// typeHelp completes the value at the marker, with typed before it, and
// returns the labels offered.
func typeHelp(t *testing.T, marker, typed string) []string {
	t.Helper()
	doc := typeHelpDoc
	for _, m := range []string{"CURSOR_REPLICAS", "CURSOR_RESTART", "CURSOR_PULL", "CURSOR_PARAM"} {
		if m != marker {
			doc = strings.Replace(doc, m, "_", 1)
		}
	}
	at := strings.Index(doc, marker)
	doc = strings.Replace(doc, marker, typed, 1)
	var labels []string
	for _, c := range CompleteFieldValue("web.cue", doc, at+len(typed), Options{}) {
		labels = append(labels, c.Label)
	}
	return labels
}

func TestTypeHelp(t *testing.T) {
	param := typeHelp(t, "CURSOR_PARAM", "")
	for _, want := range []string{"string", "int", "bool", "*default | type", `"a" | "b"`, "[...string]", "{...}", "#Probe"} {
		assert.Contains(t, param, want, "a parameter's type")
	}
	assert.NotContains(t, param, "parameter.replicas", "a parameter takes a type, not a reference")
	assert.Equal(t, []string{"int", "int & >=0"}, typeHelp(t, "CURSOR_PARAM", "in"), "filtered by what is typed")

	replicas := typeHelp(t, "CURSOR_REPLICAS", "")
	assert.Contains(t, replicas, "parameter.replicas", "an int field takes an int parameter")
	assert.NotContains(t, replicas, "parameter.image", "not a string one")
	assert.NotContains(t, replicas, "string", "an output takes values, not types")

	assert.Subset(t, typeHelp(t, "CURSOR_RESTART", ""), []string{`"Always"`, `"Never"`, `"OnFailure"`}, "the kind's enum")
	pull := typeHelp(t, "CURSOR_PULL", "")
	assert.Subset(t, pull, []string{"parameter.image", "context.name"}, "a string inside a list element")
}

// Inside a call of a package function, the value takes the input's type.
func TestTypeHelpInCalls(t *testing.T) {
	opts := functionOptions(t)
	doc := strings.Replace(functionDef("x: helpers.#Name & {name: CURSOR, suffix: \"b\"}"), "parameter: {}", "parameter: {who: string, count: int}", 1)
	at := strings.Index(doc, "CURSOR")
	doc = strings.Replace(doc, "CURSOR", "", 1)
	var labels []string
	for _, c := range CompleteFieldValue("d.cue", doc, at, opts) {
		labels = append(labels, c.Label)
	}
	assert.Contains(t, labels, "parameter.who")
	assert.NotContains(t, labels, "parameter.count")
}

// A template CUE rejects as a whole, as for a let nothing uses yet, is
// still completed: what failed is set aside, as the checks set it aside.
func TestCompleteWithAnUnusedLet(t *testing.T) {
	doc := "\"web\": {\n\ttype: \"component\"\n\tattributes: workload: definition: {apiVersion: \"apps/v1\", kind: \"Deployment\"}\n}\ntemplate: {\n\tlet tag = \"v1\"\n\toutput: {\n\t\tapiVersion: \"apps/v1\"\n\t\tkind: \"Deployment\"\n\t\tmetadata: name: CURSOR\n\t}\n\tparameter: {\n\t\timage: string\n\t\treplicas: *1 | int\n\t}\n}\n"
	labels := func(cs []Completion) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Label)
		}
		return out
	}
	src := strings.Replace(doc, "CURSOR", "parameter.", 1)
	at := strings.Index(src, "parameter.") + len("parameter.")
	assert.Equal(t, []string{"image", "replicas"}, labels(CompleteValueAt(src, at, nil)), "parameter.")

	src = strings.Replace(doc, "CURSOR", "", 1)
	at = strings.Index(src, "name: ") + len("name: ")
	assert.Contains(t, labels(CompleteFieldValue("web.cue", src, at, Options{})), "parameter.image", "a value's type help")
}
