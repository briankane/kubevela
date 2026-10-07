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

// exprDefs are definitions for checking an Application's expressions: a
// source whose schema has a host, a port and a map of labels.
func exprDefs() Options {
	src := AppDefinition{Name: "infra", Type: sourceType, Source: SourceWorkspace, CUE: "infra: {\n\ttype: \"source\"\n}\ntemplate: {\n\tschema: {\n\t\thost: string\n\t\tport: int\n\t\tlabels: [string]: string\n\t\tmeta: _\n\t}\n\tstorage: {storageTTL: \"1m\", onStaleFailure: \"use-stale\"}\n\toutput: {host: \"h\", port: 1, labels: {}, meta: {}}\n\tparameter: {zone: *\"a\" | string}\n}\n"}
	return Options{Applications: LayeredDefinitions{{src}, BuiltinDefinitions()}}
}

// exprFindings checks an Application whose web component's image is image,
// with sources, as "message".
func exprFindings(image, sources string) []string {
	app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n" + sources + "  components:\n    - name: web\n      type: webservice\n      properties:\n        image: \"" + image + "\"\n  policies:\n    - name: where\n      type: topology\n      properties:\n        clusters: [\"$(source.cfg.host)\"]\n"
	diags, _ := CheckApplicationFile("app.yaml", []byte(app), exprDefs())
	var out []string
	for _, d := range diags {
		if d.Severity == SeverityError {
			out = append(out, d.Message)
		}
	}
	return out
}

func TestApplicationExpressions(t *testing.T) {
	cfg := "  sources:\n    - name: cfg\n      type: infra\n"
	// The topology policy is built in, so reads context alone: every case
	// below has that error, and is judged by what else it has.
	policy := `"source" cannot be read here`
	check := func(name, image, sources string, want ...string) {
		t.Helper()
		got := exprFindings(image, sources)
		var rest []string
		sawPolicy := false
		for _, g := range got {
			if strings.Contains(g, policy) {
				sawPolicy = true
				continue
			}
			rest = append(rest, g)
		}
		assert.True(t, sawPolicy, "%s: a built-in policy reads context only: %v", name, got)
		if len(want) == 0 {
			assert.Empty(t, rest, name)
			return
		}
		if assert.Len(t, rest, 1, "%s: %v", name, rest) {
			for _, w := range want {
				assert.Contains(t, rest[0], w, name)
			}
		}
	}
	check("a declared source's attribute", "registry/$(source.cfg.host):1", cfg)
	check("a whole source", "$(source.cfg)", cfg)
	check("a map's entry", "$(source.cfg.labels['team'])", cfg)
	check("below a field of any type", "$(source.cfg.meta.x.y)", cfg)
	check("a source not declared", "$(source.nope.host)", cfg, "nope", "spec.sources")
	check("an attribute not in the schema", "$(source.cfg.hots)", cfg, "hots", "infra")
	check("a component not declared", "$(component.db.output.metadata.name)", cfg, "db")
	check("an expression that does not parse", "$(source.cfg.host", cfg, "unterminated")
	check("CEL that does not compile", "$(source.cfg.host +)", cfg, "")

	order := "  sources:\n    - name: first\n      type: infra\n      properties:\n        zone: \"$(source.second.host)\"\n    - name: second\n      type: infra\n"
	check("a source reading a later one", "x", order, "prior sources")
}
