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
)

// optionalDef is a component whose template body is given, with optional
// parameters param, mode, list and probe, and a required image.
func optionalDef(body string) string {
	return `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "v1", kind: "ConfigMap"}
` + body + `
	parameter: {
		image:  string
		param?: string
		mode?:  "a" | "b"
		list?: [...string]
		#Probe: {path: string, port?: int}
		probe?: #Probe
		count:  *1 | int
	}
}
`
}

// ungated is what the check finds in body, as "line: message".
func ungated(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, d := range AnalyzeWith("web.cue", []byte(optionalDef(body)), Options{}).Diagnostics {
		if strings.Contains(d.Message, "is optional:") {
			out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
		}
	}
	return out
}

// Every read in the template is checked, rendered or not: one nothing uses
// yet fails the render once something does. The safe forms were checked
// against vela dry-run with the parameter left out.
func TestOptionalReferences(t *testing.T) {
	flagged := map[string]string{
		"a helper nothing uses":                 "\tvalue: {inner: parameter.param}",
		"a hidden helper nothing uses":          "\t_value: parameter.param",
		"a let nothing uses":                    "\tlet v = parameter.param",
		"a read by index":                       "\toutput: data: inner: parameter[\"param\"]",
		"a read in output":                      "\toutput: data: inner: parameter.param",
		"a read in an outputs object":           "\toutputs: x: {apiVersion: \"v1\", kind: \"ConfigMap\", data: v: parameter.param}",
		"a field of an optional struct":         "\toutput: data: path: parameter.probe.path",
		"a comparison inside output":            "\toutput: data: {if parameter.mode == \"a\" {v: \"1\"}}",
		"a comparison after && its guard":       "\toutput: data: {if parameter.mode != _|_ && parameter.mode == \"a\" {v: \"1\"}}",
		"a for inside output":                   "\toutput: data: {for x in parameter.list {\"\\(x)\": x}}",
		"through a helper output uses":          "\tvalue: {inner: parameter.param}\n\toutput: data: value",
		"through a let output uses":             "\tlet v = parameter.param\n\toutput: data: x: v",
		"an optional field of a guarded struct": "\toutput: data: {if parameter.probe != _|_ {v: parameter.probe.port}}",
	}
	for name, body := range flagged {
		got := ungated(t, body)
		if assert.Len(t, got, 1, "%s: %v", name, got) {
			assert.Contains(t, got[0], "7: ", name)
		}
	}
	clean := map[string]string{
		"guarded by index":              "\toutput: data: {if parameter[\"param\"] != _|_ {inner: parameter.param}}",
		"guarded":                       "\toutput: data: {if parameter.param != _|_ {inner: parameter.param}}",
		"guarded by an ancestor":        "\toutput: data: {if parameter.probe != _|_ {v: parameter.probe.path}}",
		"guarded with others":           "\toutput: data: {if parameter.param != _|_ && parameter.probe != _|_ {v: parameter.param}}",
		"a test of a value":             "\toutput: data: {if (parameter.mode & \"a\") != _|_ {v: \"1\"}}",
		"a default":                     "\toutput: data: v: *parameter.param | \"x\"",
		"nested ifs":                    "\toutput: data: {\n\t\tif parameter.mode != _|_ {\n\t\t\tif parameter.mode == \"a\" {v: \"1\"}\n\t\t}\n\t}",
		"a required parameter":          "\toutput: data: v: parameter.image",
		"a defaulted parameter":         "\toutput: data: v: parameter.count",
		"a guard on a for":              "\toutput: data: {if parameter.list != _|_ {for x in parameter.list {\"\\(x)\": x}}}",
		"an optional field":             "\toutput: data: v?: parameter.param",
		"a for choosing outputs":        "\toutputs: {for x in parameter.list {\"\\(x)\": {apiVersion: \"v1\", kind: \"ConfigMap\"}}}",
		"an if choosing outputs":        "\toutputs: {if parameter.mode == \"a\" {y: {apiVersion: \"v1\", kind: \"ConfigMap\"}}}",
		"an if at the template's level": "\tif parameter.mode == \"a\" {\n\t\toutputs: y: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\t}",
	}
	for name, body := range clean {
		assert.Empty(t, ungated(t, body), name)
	}
}

// The fix wraps the field reading it in a guard: on lines of their own for a
// field that starts its line, in place for one inside a struct written on
// one line.
func TestOptionalReferenceFix(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"a field on its own line": {
			body: "\toutput: data: {\n\t\tinner: parameter.param\n\t}",
			want: "\toutput: data: {\n\t\tif parameter.param != _|_ {\n\t\t\tinner: parameter.param\n\t\t}\n\t}\n",
		},
		"a field inside a struct on one line": {
			body: "\toutput: data: {inner: parameter.param}",
			want: "\toutput: data: {if parameter.param != _|_ {inner: parameter.param}}\n",
		},
	}
	for name, c := range cases {
		src := optionalDef(c.body)
		var fix *Fix
		for _, d := range AnalyzeWith("web.cue", []byte(src), Options{}).Diagnostics {
			if strings.Contains(d.Message, "is optional:") && len(d.Fixes) > 0 {
				fix = &d.Fixes[0]
			}
		}
		if !assert.NotNil(t, fix, name) {
			continue
		}
		fixed := applyRangeEdits(src, fix.Edits)
		assert.Contains(t, fixed, c.want, name)
		for _, d := range AnalyzeWith("web.cue", []byte(fixed), Options{}).Diagnostics {
			assert.NotContains(t, d.Message, "is optional:", "%s: the fixed template is gated:\n%s", name, fixed)
		}
	}
}

// The parameter's schema is read without touching the template's syntax:
// a parameter that refers outside itself, to a helper of the template, once
// left the template's references bound to another compile, and the next
// check panicked.
func TestParameterSchemaLeavesTheTemplateAlone(t *testing.T) {
	src := `"web": {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	_default: "a"
	output: {apiVersion: "apps/v1", kind: "Deployment", spec: selector: matchLabels: app: parameter.name}
	parameter: {
		name: *_default | string
		#Probe: {path: string}
		probe?: #Probe
	}
}
`
	assert.NotPanics(t, func() { AnalyzeWith("web.cue", []byte(src), Options{}) })
}
