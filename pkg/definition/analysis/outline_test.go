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

func TestOutline(t *testing.T) {
	syms, ok := Outline("def.cue", navDoc)
	require.True(t, ok)
	require.Len(t, syms, 2)

	header := syms[0]
	assert.Equal(t, "web", header.Name)
	assert.Equal(t, "component", header.Detail)
	assert.Equal(t, SymbolDefinition, header.Kind)

	tmpl := syms[1]
	assert.Equal(t, "template", tmpl.Name)
	byName := map[string]Symbol{}
	for _, c := range tmpl.Children {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "parameter")
	require.Contains(t, byName, "output")
	require.Contains(t, byName, "_labels")
	assert.Equal(t, SymbolHelper, byName["_labels"].Kind)

	params := map[string]Symbol{}
	for _, c := range byName["parameter"].Children {
		params[c.Name] = c
	}
	require.Contains(t, params, "image")
	assert.Equal(t, SymbolParameter, params["image"].Kind)
	assert.Equal(t, "The image to run", params["image"].Detail)
	assert.Equal(t, 20, params["image"].Selection.Start.Line)
	assert.Contains(t, params, "replicas")

	var outputs []string
	for _, c := range byName["outputs"].Children {
		outputs = append(outputs, c.Name)
	}
	assert.Equal(t, []string{"tagged"}, outputs)
	assert.Equal(t, "apps/v1 Deployment", byName["output"].Detail, "an object says its kind")
}

// A pattern's label is an expression, not a name: it is named as written,
// as an editor rejects a symbol with no name.
func TestOutlineNamesPatterns(t *testing.T) {
	src := "\"web\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: {\n\t\tlabels?: [string]: string\n\t\t[=~\"^x-\"]: int\n\t}\n}\n"
	syms, ok := Outline("web.cue", src)
	require.True(t, ok)
	var names []string
	var walk func([]Symbol)
	walk = func(ss []Symbol) {
		for _, s := range ss {
			assert.NotEmpty(t, strings.TrimSpace(s.Name), "every symbol is named")
			names = append(names, s.Name)
			walk(s.Children)
		}
	}
	walk(syms)
	assert.Contains(t, names, "[string]")
	assert.Contains(t, names, `[=~"^x-"]`)
}
