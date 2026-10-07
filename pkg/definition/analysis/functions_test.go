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

// functionPackages hold a provider function, #Greet, which takes $params,
// and a function of plain CUE, #Name, which takes name and suffix and
// derives out.
const functionPackages = `apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata:
  name: greeter
spec:
  path: ext/greeter
  provider:
    protocol: http
    endpoint: http://greeter.example.com
  templates:
    greeter.cue: |
      package greeter

      // +usage=Greets a name
      #Greet: {
        #do:       "greet"
        #provider: "greeter"
        $params: {
          // +usage=Who to greet
          name: string
        }
        $returns: {
          // +usage=The greeting
          message: string
        }
      }
---
apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata:
  name: helpers
spec:
  path: ext/helpers
  templates:
    helpers.cue: |
      package helpers

      // +usage=Joins a name and a suffix with a hyphen
      #Name: {
        name:   string
        suffix: string
        out:    name + "-" + suffix
      }
`

// functionDef is a workflow step calling into functionPackages, its
// template's body given.
func functionDef(body string) string {
	return "import (\n\t\"ext/greeter\"\n\t\"ext/helpers\"\n)\n\n\"d\": {\n\ttype: \"workflow-step\"\n}\ntemplate: {\n\t" + body + "\n\tparameter: {}\n}\n"
}

func functionOptions(t *testing.T) Options {
	t.Helper()
	pkgs, errs := ParsePackages([]byte(functionPackages))
	require.Empty(t, errs)
	return Options{Externals: NewExternals(pkgs)}
}

func TestFunctionMembers(t *testing.T) {
	opts := functionOptions(t)
	members := func(pkg string) map[string]Completion {
		src := functionDef("x: " + pkg + ".#")
		out := map[string]Completion{}
		for _, c := range CompletePackageMemberWith(src, "\tx: "+pkg+".#", opts.Externals) {
			out[c.Label] = c
		}
		return out
	}

	name := members("helpers")["#Name"]
	assert.Equal(t, "{name: string, suffix: string} → out", name.Detail)
	assert.Contains(t, name.Doc, "Joins a name and a suffix")
	assert.Contains(t, name.Doc, "```cue")
	assert.Contains(t, name.Snippet, "name: ${1}")
	assert.Contains(t, name.Snippet, "suffix: ${2}")
	assert.NotContains(t, name.Snippet, "$params", "#Name takes its fields, not $params")
	assert.NotContains(t, name.Snippet, "out:", "out is derived")

	greet := members("greeter")["#Greet"]
	assert.Equal(t, "$params: {name: string} → $returns: {message: string}", greet.Detail)
	assert.Contains(t, greet.Doc, "Greets a name")
	assert.Contains(t, greet.Snippet, "\\$params: {")
	assert.Contains(t, greet.Snippet, "name: ${1}")
}

func TestHoverFunction(t *testing.T) {
	opts := functionOptions(t)
	src := functionDef(`x: helpers.#Name & {name: "a", suffix: "b"}`)
	h, ok := Hover(src, strings.Index(src, "#Name")+2, opts)
	require.True(t, ok)
	assert.Contains(t, h, "name: string")
	assert.Contains(t, h, "Joins a name")
}

func TestCompleteInCall(t *testing.T) {
	opts := functionOptions(t)
	cases := map[string]struct {
		body, marker string
		want         []string
	}{
		"a plain function's inputs":             {body: "x: helpers.#Name & {\n\t\t\n\t}", marker: "helpers.#Name & {\n\t\t", want: []string{"name", "suffix"}},
		"a plain function, typing":              {body: "x: helpers.#Name & {na}", marker: "{na", want: []string{"name"}},
		"those not yet given":                   {body: "x: helpers.#Name & {\n\t\tname: \"a\"\n\t\t\n\t}", marker: "name: \"a\"\n\t\t", want: []string{"suffix"}},
		"a provider function's call":            {body: "x: greeter.#Greet & {\n\t\t\n\t}", marker: "greeter.#Greet & {\n\t\t", want: []string{"$params"}},
		"a provider function's $params":         {body: "x: greeter.#Greet & {$params: {\n\t\t\n\t}}", marker: "$params: {\n\t\t", want: []string{"name"}},
		"a provider function's $params, typing": {body: "x: greeter.#Greet & {$params: {na}}", marker: "{na", want: []string{"name"}},
	}
	for name, c := range cases {
		src := functionDef(c.body)
		at := strings.Index(src, c.marker) + len(c.marker)
		var got []string
		for _, it := range CompleteInCall(src, at, opts.Externals) {
			got = append(got, it.Label)
		}
		assert.Equal(t, c.want, got, name)
	}
}

func TestCallOfAPlainFunctionMissingAnInput(t *testing.T) {
	opts := functionOptions(t)
	var errs []string
	for _, d := range AnalyzeWith("d.cue", []byte(functionDef(`x: helpers.#Name & {name: "a"}`)), opts).Diagnostics {
		if d.Severity == SeverityError {
			errs = append(errs, d.Message)
		}
	}
	assert.Equal(t, []string{"helpers.#Name needs suffix"}, errs)
	for _, d := range AnalyzeWith("d.cue", []byte(functionDef(`x: helpers.#Name & {name: "a", suffix: "b"}`)), opts).Diagnostics {
		assert.NotEqual(t, SeverityError, d.Severity, d.Message)
	}
}

// The legacy vela/op functions are provider calls without $params, whose
// inputs and results are fields alike: none is told required.
func TestLegacyProviderFunctions(t *testing.T) {
	src := "import \"vela/op\"\n\n\"d\": {\n\ttype: \"workflow-step\"\n}\ntemplate: {\n\treq: op.#HTTPGet & {url: \"http://x\"}\n\tparameter: {}\n}\n"
	for _, d := range Analyze("d.cue", []byte(src)).Diagnostics {
		assert.NotContains(t, d.Message, "needs")
	}
	for _, c := range CompletePackageMember(src, "\treq: op.#HTTPG") {
		assert.Equal(t, "#HTTPGet & {\n\t$0\n}", c.Snippet)
	}
}
