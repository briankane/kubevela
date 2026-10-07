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
	src := AppDefinition{Name: "infra", Type: sourceType, Source: SourceWorkspace, CUE: "infra: {\n\ttype: \"source\"\n}\ntemplate: {\n\tschema: {\n\t\thost: string\n\t\tport: int\n\t\tlabels: [string]: string\n\t\tmeta: _\n\t\tnote?: string\n\t}\n\tstorage: {storageTTL: \"1m\", onStaleFailure: \"use-stale\"}\n\toutput: {host: \"h\", port: 1, labels: {}, meta: {}}\n\tparameter: {zone: *\"a\" | string}\n}\n"}
	svc := AppDefinition{Name: "svc", Type: componentType, Source: SourceWorkspace, CUE: "svc: {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: {\n\t\timage: string\n\t\treplicas: *1 | int\n\t\ttags?: [...string]\n\t}\n}\n"}
	return Options{Applications: LayeredDefinitions{{src, svc}, BuiltinDefinitions()}}
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
	check("a whole source, an object, into a string", "$(source.cfg)", cfg, "type mismatch", "object")
	check("a map's entry, unguarded, into a required parameter", "$(source.cfg.labels['team'])", cfg, "may be absent")
	check("a map's entry, guarded", "$(has(source.cfg.labels.team) ? source.cfg.labels['team'] : 'none')", cfg)
	check("below a field of any type", "$(source.cfg.meta.x.y)", cfg)
	check("a source not declared", "$(source.nope.host)", cfg, "nope", "spec.sources")
	check("an attribute not in the schema", "$(source.cfg.hots)", cfg, "hots", "infra")
	check("a component not declared", "$(component.db.output.metadata.name)", cfg, "db")
	check("an expression that does not parse", "$(source.cfg.host", cfg, "unterminated")
	check("CEL that does not compile", "$(source.cfg.host +)", cfg, "")

	order := "  sources:\n    - name: first\n      type: infra\n      properties:\n        zone: \"$(source.second.host)\"\n    - name: second\n      type: infra\n"
	check("a source reading a later one", "x", order, "prior sources")
}

// Inside $( ), completion offers what the expression can read there.
func TestCompleteAppExpressions(t *testing.T) {
	app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  sources:\n    - name: cfg\n      type: infra\n    - name: second\n      type: infra\n      properties:\n        zone: \"SOURCE_PROP\n  components:\n    - name: web\n      type: webservice\n      properties:\n        image: \"COMP_PROP\n    - name: db\n      type: webservice\n"
	complete := func(marker, typed string) []string {
		doc := strings.Replace(app, marker, typed, 1)
		for _, m := range []string{"SOURCE_PROP", "COMP_PROP"} {
			doc = strings.Replace(doc, m, "x\"", 1)
		}
		at := strings.Index(doc, typed) + len(typed)
		got, _ := CompleteYAMLFile("app.yaml", doc, at, exprDefs())
		var out []string
		for _, c := range got {
			out = append(out, c.Label)
		}
		return out
	}
	assert.Equal(t, []string{"component", "context", "source"}, complete("COMP_PROP", "$("), "a component's roots")
	assert.Equal(t, []string{"cfg", "second"}, complete("COMP_PROP", "$(source."))
	assert.Equal(t, []string{"host"}, complete("COMP_PROP", "$(source.cfg.ho"))
	assert.Equal(t, []string{"cfg"}, complete("SOURCE_PROP", "$(source."), "a source reads only those before it")
	assert.Equal(t, []string{"context", "source"}, complete("SOURCE_PROP", "$("), "a source reads no component")
	assert.Contains(t, complete("COMP_PROP", "$(context."), "appName")
	assert.Equal(t, []string{"db"}, complete("COMP_PROP", "$(component."), "the other components")
	assert.Equal(t, []string{"output", "outputs"}, complete("COMP_PROP", "$(component.db."))
	assert.Contains(t, complete("COMP_PROP", "registry/$(source.cfg.h"), "host", "embedded in text")
}

// An expression's result is typed against the parameter it feeds, as
// admission types it.
func TestApplicationExpressionTypes(t *testing.T) {
	check := func(prop, value string) []string {
		app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  sources:\n    - name: cfg\n      type: infra\n  components:\n    - name: web\n      type: svc\n      properties:\n        image: x\n        " + prop + ": " + value + "\n"
		if prop == "image" {
			app = strings.Replace(app, "        image: x\n", "", 1)
		}
		diags, _ := CheckApplicationFile("app.yaml", []byte(app), exprDefs())
		var out []string
		for _, d := range diags {
			if d.Severity == SeverityError {
				out = append(out, d.Message)
			}
		}
		return out
	}
	assert.Empty(t, check("replicas", `"$(source.cfg.port)"`), "an int into an int")
	assert.Empty(t, check("image", `"registry/$(source.cfg.port)"`), "embedded in text, a string")
	assert.Empty(t, check("image", `"$(has(source.cfg.note) ? source.cfg.note : 'none')"`), "a guarded optional read")

	got := check("replicas", `"$(source.cfg.host)"`)
	if assert.Len(t, got, 1) {
		assert.Contains(t, got[0], "type mismatch")
		assert.Contains(t, got[0], `component "svc" parameter expects int`)
	}
	got = check("image", `"$(source.cfg.note)"`)
	if assert.Len(t, got, 1) {
		assert.Contains(t, got[0], "may be absent and feeds required")
		assert.Contains(t, got[0], "has(")
	}
	got = check("tags", `"$(source.cfg.host)"`)
	if assert.Len(t, got, 1) {
		assert.Contains(t, got[0], "type mismatch")
	}
	assert.Empty(t, check("image", `"$(context.appName)"`), "a context field the component reads")
	got = check("image", `"$(context.nope)"`)
	if assert.Len(t, got, 1, "a context field no component gets") {
		assert.Contains(t, got[0], "nope")
	}
	got = check("image", `"$(source.cfg.labels)"`)
	assert.Len(t, got, 1, "a map into a string: %v", got)
}

// A property the item's definition does not declare is refused, as admission
// refuses it, when that definition's parameter is empty too.
func TestPropertyOfAnEmptyParameter(t *testing.T) {
	empty := AppDefinition{Name: "facts", Type: sourceType, Source: SourceWorkspace, CUE: "facts: {\n\ttype: \"source\"\n}\ntemplate: {\n\tschema: host: string\n\toutput: host: \"h\"\n\tparameter: {}\n}\n"}
	opts := Options{Applications: LayeredDefinitions{{empty}, BuiltinDefinitions()}}
	app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  sources:\n    - name: f\n      type: facts\n      properties:\n        zone: eu\n  components:\n    - name: web\n      type: webservice\n      properties:\n        image: x\n"
	diags, _ := CheckApplicationFile("app.yaml", []byte(app), opts)
	var msgs []string
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	assert.Contains(t, strings.Join(msgs, "\n"), "zone", "%v", msgs)

	// An expression's own fault at the same property does not hide it.
	app = strings.Replace(app, "zone: eu", "zone: \"$(source.later.host)\"", 1)
	diags, _ = CheckApplicationFile("app.yaml", []byte(app), opts)
	msgs = nil
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	all := strings.Join(msgs, "\n")
	assert.Contains(t, all, "takes no parameter zone", "%v", msgs)
	assert.Contains(t, all, `"later" is not declared`, "%v", msgs)
}
