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
)

const sourceDefinition = "infra: {\n\ttype: \"source\"\n}\ntemplate: {\n\tschema: host: string\n\toutput: host: \"h\"\n\tparameter: {}\n}\n"

// gateFindings are the findings about feature gates, as "line: message".
func gateFindings(diags []Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if strings.Contains(d.Message, "Enable") || strings.Contains(d.Message, "Require") {
			out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
		}
	}
	return out
}

func gates(on map[string]bool, missing ...string) *ControllerGates {
	g := &ControllerGates{Context: "k3d-vela", Version: "v1.11.0", On: on, Missing: map[string]bool{}}
	for _, m := range missing {
		g.Missing[m] = true
	}
	return g
}

func TestDefinitionGates(t *testing.T) {
	extending := child("web", "\toutput: $super.output\n\tparameter: $super.parameter\n")
	cases := map[string]struct {
		src   string
		gates *ControllerGates
		want  []string
	}{
		"extends with the gate off": {
			src:   extending,
			gates: gates(map[string]bool{"EnableDefinitionInheritance": false}),
			want:  []string{"3: EnableDefinitionInheritance is off on k3d-vela: the controller refuses a definition that sets extends"},
		},
		"extends with the gate on":      {src: extending, gates: gates(map[string]bool{"EnableDefinitionInheritance": true})},
		"extends, the gate not known":   {src: extending, gates: gates(map[string]bool{})},
		"extends, the cluster not read": {src: extending},
		"extends on a controller without the gate": {
			src:   extending,
			gates: gates(map[string]bool{}, "EnableDefinitionInheritance"),
			want:  []string{"3: the controller on k3d-vela (v1.11.0) has no EnableDefinitionInheritance, so it does not support extends"},
		},
		"a global Application-scoped policy, both gates off": {
			src:   globalPolicy,
			gates: gates(map[string]bool{"EnableApplicationScopedPolicies": false, "EnableGlobalPolicies": false}),
			want: []string{
				"4: EnableApplicationScopedPolicies is off on k3d-vela: the controller does not apply a policy with scope: Application",
				"5: EnableGlobalPolicies is off on k3d-vela: the controller does not discover a policy with global: true",
			},
		},
		"a SourceDefinition, expressions off": {
			src:   sourceDefinition,
			gates: gates(map[string]bool{"EnableCelExpressions": false}),
			want:  []string{"1: EnableCelExpressions is off on k3d-vela: the controller reads no SourceDefinition"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := AnalyzeWith("def.cue", []byte(tc.src), Options{Gates: tc.gates})
			require.True(t, res.IsDefinition)
			assert.Equal(t, tc.want, gateFindings(res.Diagnostics))
		})
	}
}

// A gate that is off is turned on from the Controller panel.
func TestGateFix(t *testing.T) {
	res := AnalyzeWith("def.cue", []byte(globalPolicy), Options{Gates: gates(map[string]bool{"EnableGlobalPolicies": false})})
	var fixes []Fix
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "EnableGlobalPolicies") {
			fixes = d.Fixes
		}
	}
	require.Len(t, fixes, 1)
	assert.Equal(t, "Turn on EnableGlobalPolicies in the controller's feature gates", fixes[0].Title)
	assert.Equal(t, &FixCommand{Name: "kubevela.controller.open", Arguments: []interface{}{map[string]interface{}{"gate": "EnableGlobalPolicies", "value": true}}}, fixes[0].Command)
	assert.Empty(t, fixes[0].Edits)
}

func TestApplicationGates(t *testing.T) {
	withExpr := strings.Replace(goodApp, "image: nginx", "image: $(context.namespace)", 1)
	optedIn := strings.Replace(withExpr, "  name: demo\n", "  name: demo\n  annotations:\n    app.oam.dev/cel-expressions: \"true\"\n", 1)
	addon := strings.Replace(goodApp, "type: webservice", "type: addon", 1)
	withSource := strings.Replace(goodApp, "spec:\n", "spec:\n  sources:\n    - name: cfg\n      type: http-get\n", 1)
	cases := map[string]struct {
		src  string
		on   map[string]bool
		want []string
	}{
		"an expression, expressions off": {
			src:  withExpr,
			on:   map[string]bool{"EnableCelExpressions": false},
			want: []string{"10: EnableCelExpressions is off on k3d-vela: the controller does not read $( ) expressions"},
		},
		"an expression, not opted in": {
			src:  withExpr,
			on:   map[string]bool{"EnableCelExpressions": true, "RequireCelExpressionOptIn": true},
			want: []string{"10: RequireCelExpressionOptIn is on at k3d-vela: expressions are read only in an Application annotated app.oam.dev/cel-expressions: \"true\""},
		},
		"an expression, opted in":         {src: optedIn, on: map[string]bool{"EnableCelExpressions": true, "RequireCelExpressionOptIn": true}},
		"an expression, no opt-in needed": {src: withExpr, on: map[string]bool{"EnableCelExpressions": true, "RequireCelExpressionOptIn": false}},
		"a source, expressions off": {
			src:  withSource,
			on:   map[string]bool{"EnableCelExpressions": false},
			want: []string{"8: EnableCelExpressions is off on k3d-vela: the controller reads no SourceDefinition"},
		},
		"an addon component, the gate off": {
			src:  addon,
			on:   map[string]bool{"EnableAddonComponent": false},
			want: []string{"8: EnableAddonComponent is off on k3d-vela: an Application with type: addon fails at render"},
		},
		"an addon component, the gate on": {src: addon, on: map[string]bool{"EnableAddonComponent": true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diags, ok := CheckApplicationFile("app.yaml", []byte(tc.src), Options{Gates: gates(tc.on)})
			require.True(t, ok)
			assert.Equal(t, tc.want, gateFindings(diags))
		})
	}
}
