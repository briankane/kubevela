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

const makeParamDef = `web: {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: labels: "app.kubernetes.io/part-of": "shop"
		spec: {
			replicas: 2
			paused:   false
			template: spec: containers: [{image: "nginx:1.27", name: "web"}]
		}
	}
	parameter: {
		// +usage=Name of the container
		name: *"web" | string
	}
}
`

// actionAt is the parameter actions at the first occurrence of marker, after its prefix.
func actionAt(t *testing.T, src, marker string) []Fix {
	t.Helper()
	at := strings.Index(src, marker)
	require.GreaterOrEqual(t, at, 0, marker)
	return ParameterActions("web.cue", src, at+len(marker)-1)
}

func TestMakeParameter(t *testing.T) {
	fixes := actionAt(t, makeParamDef, "replicas: 2")
	require.Len(t, fixes, 1)
	assert.Equal(t, "Make replicas a parameter", fixes[0].Title)
	got := applyRangeEdits(makeParamDef, fixes[0].Edits)
	assert.Contains(t, got, "\t\t\treplicas: parameter.replicas\n")
	assert.Contains(t, got, "\t\tname: *\"web\" | string\n\t\t// +usage=Replicas of the output\n\t\treplicas: *2 | int\n\t}\n")
	for _, d := range Analyze("web.cue", []byte(got)).Diagnostics {
		assert.NotEqual(t, SeverityError, d.Severity, "the result checks clean: %s", d.Message)
	}

	t.Run("each kind of literal", func(t *testing.T) {
		paused := applyRangeEdits(makeParamDef, actionAt(t, makeParamDef, "paused:   false")[0].Edits)
		assert.Contains(t, paused, "paused: *false | bool")
		image := applyRangeEdits(makeParamDef, actionAt(t, makeParamDef, `image: "nginx:1.27"`)[0].Edits)
		assert.Contains(t, image, `image: *"nginx:1.27" | string`)
		assert.Contains(t, image, `[{image: parameter.image, name: "web"}]`)
	})

	t.Run("a quoted label makes a name a reference can use", func(t *testing.T) {
		fixes := actionAt(t, makeParamDef, `"app.kubernetes.io/part-of": "shop"`)
		require.NotEmpty(t, fixes)
		assert.Equal(t, "Make partOf a parameter", fixes[0].Title)
	})

	t.Run("a name the parameters have", func(t *testing.T) {
		fixes := actionAt(t, makeParamDef, `name: "web"`)
		require.Len(t, fixes, 2)
		assert.Equal(t, "Use parameter.name", fixes[0].Title, "the same type: reuse it")
		assert.Contains(t, applyRangeEdits(makeParamDef, fixes[0].Edits), "name: parameter.name}]")
		assert.Equal(t, "Make name2 a parameter", fixes[1].Title)
	})

	t.Run("no parameter block yet", func(t *testing.T) {
		src := makeParamDef[:strings.Index(makeParamDef, "\tparameter: {")] + "}\n"
		fixes := actionAt(t, src, "replicas: 2")
		require.Len(t, fixes, 1)
		got := applyRangeEdits(src, fixes[0].Edits)
		assert.Contains(t, got, "template: {\n\tparameter: {\n\t\t// +usage=Replicas of the output\n\t\treplicas: *2 | int\n\t}\n")
	})

	t.Run("nothing outside the output, or off a literal", func(t *testing.T) {
		assert.Empty(t, actionAt(t, makeParamDef, `*"web"`), "a parameter's own default")
		assert.Empty(t, actionAt(t, makeParamDef, `type: "component"`), "the header")
		assert.Empty(t, actionAt(t, makeParamDef, "spec: {"), "a struct")
	})
}
