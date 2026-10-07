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

// traitDefs are definitions for checking the traits an Application attaches.
func traitDefs() Options {
	comp := func(name, attrs, output string) AppDefinition {
		return AppDefinition{Name: name, Type: componentType, Source: SourceWorkspace, CUE: "\"" + name + "\": {\n\ttype: \"component\"\n\tattributes: " + attrs + "\n}\ntemplate: {\n\toutput: " + output + "\n\tparameter: {}\n}\n"}
	}
	trait := func(name, header string) AppDefinition {
		return AppDefinition{Name: name, Type: traitType, Source: SourceWorkspace, CUE: "\"" + name + "\": {\n\ttype: \"trait\"\n" + header + "}\ntemplate: {\n\tpatch: {}\n\tparameter: {}\n}\n"}
	}
	return Options{Applications: LayeredDefinitions{{
		comp("web", `workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}`, `{apiVersion: "apps/v1", kind: "Deployment"}`),
		comp("cm", `workload: type: "autodetects.core.oam.dev"`, `{apiVersion: "v1", kind: "ConfigMap"}`),
		trait("scaler", "\tattributes: appliesToWorkloads: [\"deployments.apps\"]\n"),
		trait("for-web", "\tattributes: appliesToWorkloads: [\"web\"]\n"),
		trait("anywhere", "\tattributes: appliesToWorkloads: [\"*\"]\n"),
		trait("a", "\tattributes: conflictsWith: [\"b\"]\n"),
		trait("b", ""),
		trait("alone", "\tattributes: conflictsWith: [\"*\"]\n"),
		trait("netpol", "\tlabels: {area: \"network\"}\n"),
		trait("net-guard", "\tattributes: conflictsWith: [\"labelSelector:area=network\"]\n"),
	}}}
}

// traitFindings checks an Application of one component of type comp with
// traits, as "trait index severity: message".
func traitFindings(comp string, traits ...string) []string {
	var b strings.Builder
	b.WriteString("apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  components:\n    - name: c\n      type: " + comp + "\n      traits:\n")
	for _, t := range traits {
		b.WriteString("        - type: " + t + "\n")
	}
	diags, _ := CheckApplicationFile("app.yaml", []byte(b.String()), traitDefs())
	var out []string
	for _, d := range diags {
		if strings.Contains(d.Message, "conflicts") || strings.Contains(d.Message, "applies to") {
			sev := map[Severity]string{SeverityError: "error", SeverityWarning: "warning"}[d.Severity]
			out = append(out, strings.Repeat(" ", 0)+string(rune('0'+d.Range.Start.Line-10))+" "+sev+": "+d.Message)
		}
	}
	return out
}

func TestApplicationTraits(t *testing.T) {
	cases := map[string]struct {
		comp   string
		traits []string
		want   []string
	}{
		"traits that fit":                          {comp: "web", traits: []string{"scaler", "for-web", "anywhere", "b"}},
		"a trait for other workloads":              {comp: "cm", traits: []string{"scaler"}, want: []string{"0 warning", "scaler applies to deployments.apps", "configmaps"}},
		"a trait for another component type":       {comp: "cm", traits: []string{"for-web"}, want: []string{"0 warning", "for-web applies to web"}},
		"a conflict, declared by the first":        {comp: "web", traits: []string{"a", "b"}, want: []string{"1 error", `trait "a" conflicts with trait "b"`}},
		"a conflict, declared by the second":       {comp: "web", traits: []string{"b", "a"}, want: []string{"1 error", `"b" conflicts with trait "a"`}},
		"a trait that conflicts with any other":    {comp: "web", traits: []string{"b", "alone"}, want: []string{"1 error", "alone"}},
		"a conflict by label":                      {comp: "web", traits: []string{"netpol", "net-guard"}, want: []string{"1 error", "net-guard"}},
		"no conflict with a trait of other labels": {comp: "web", traits: []string{"b", "net-guard"}},
	}
	for name, c := range cases {
		got := traitFindings(c.comp, c.traits...)
		if len(c.want) == 0 {
			assert.Empty(t, got, name)
			continue
		}
		if assert.Len(t, got, 1, "%s: %v", name, got) {
			for _, w := range c.want {
				assert.Contains(t, got[0], w, name)
			}
		}
	}
}
