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
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goodApp's lines are counted on below.
const goodApp = `apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: demo
spec:
  components:
    - name: web
      type: webservice
      properties:
        image: nginx
        ports:
          - port: 80
            expose: true
      traits:
        - type: scaler
          properties:
            replicas: 2
  policies:
    - name: where
      type: topology
      properties:
        clusters: ["local"]
`

func builtinOnly() Options {
	return Options{Applications: LayeredDefinitions{BuiltinDefinitions()}}
}

// checkApp reports what checking src as an Application finds, as
// "line severity: message".
func checkApp(t *testing.T, src string, opts Options) []string {
	t.Helper()
	diags, ok := CheckApplicationFile("app.yaml", []byte(src), opts)
	require.True(t, ok, "an Application")
	var out []string
	for _, d := range diags {
		if d.Severity == SeverityInfo {
			continue
		}
		sev := map[Severity]string{SeverityError: "error", SeverityWarning: "warning"}[d.Severity]
		out = append(out, strconv.Itoa(d.Range.Start.Line)+" "+sev+": "+d.Message)
	}
	return out
}

func TestApplication(t *testing.T) {
	cases := map[string]struct {
		edit func(string) string
		want []string
	}{
		"a valid Application": {edit: func(s string) string { return s }},
		"a component type no definition has": {
			edit: func(s string) string { return strings.Replace(s, "type: webservice", "type: websrvice", 1) },
			want: []string{"8 warning:", "websrvice"},
		},
		"a property its definition does not take": {
			edit: func(s string) string { return strings.Replace(s, "image: nginx", "imge: nginx", 1) },
			want: []string{"10 error:", "webservice takes no parameter imge"},
		},
		"a property of the wrong type": {
			edit: func(s string) string { return strings.Replace(s, "replicas: 2", "replicas: two", 1) },
			want: []string{"17 error:", "replicas"},
		},
		"a required property missing": {
			edit: func(s string) string { return strings.Replace(s, "        image: nginx\n", "", 1) },
			want: []string{"8 error:", "webservice requires image"},
		},
		"a field the Application does not have": {
			edit: func(s string) string { return strings.Replace(s, "  policies:", "  policys:", 1) },
			want: []string{"18 error:", "policys"},
		},
		"a nested property its definition does not take": {
			edit: func(s string) string { return strings.Replace(s, "expose: true", "exposed: true", 1) },
			want: []string{"13 error:", "exposed"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkApp(t, tc.edit(goodApp), builtinOnly())
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got, "want %v", tc.want)
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}

// A workspace definition takes precedence over the built-in of its name.
func TestApplicationPrefersTheWorkspace(t *testing.T) {
	local := "\"webservice\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: {\n\t\t// +usage=The only thing it takes\n\t\tcolour: *\"red\" | string\n\t}\n}\n"
	def, ok := AppDefinitionOf("webservice.cue", local, SourceWorkspace)
	require.True(t, ok)
	opts := Options{Applications: LayeredDefinitions{{def}, BuiltinDefinitions()}}
	got := strings.Join(checkApp(t, goodApp, opts), "\n")
	assert.Contains(t, got, "webservice takes no parameter image", "the workspace's webservice, not the built-in")
}

func TestApplicationFixes(t *testing.T) {
	src := strings.Replace(goodApp, "type: webservice", "type: websrvice", 1)
	diags, _ := CheckApplicationFile("app.yaml", []byte(src), builtinOnly())
	var titles []string
	for _, d := range diags {
		for _, f := range d.Fixes {
			titles = append(titles, f.Title)
			if f.Title == "Change to webservice" {
				assert.Contains(t, applyRangeEdits(src, f.Edits), "      type: webservice\n")
			}
		}
	}
	assert.Contains(t, titles, "Change to webservice")

	src = strings.Replace(goodApp, "        image: nginx\n", "", 1)
	diags, _ = CheckApplicationFile("app.yaml", []byte(src), builtinOnly())
	found := false
	for _, d := range diags {
		for _, f := range d.Fixes {
			if f.Title == "Add the required properties" {
				found = true
				assert.Contains(t, applyRangeEdits(src, f.Edits), "      properties:\n        image: \n        ports:")
			}
		}
	}
	assert.True(t, found, "a fix adds what is required")

	_, ok := CheckApplicationFile("cm.yaml", []byte("apiVersion: v1\nkind: ConfigMap\n"), builtinOnly())
	assert.False(t, ok, "not an Application")
}

// completeApp completes src, an Application, at its "|".
func completeApp(t *testing.T, src string) []Completion {
	t.Helper()
	cursor := strings.Index(src, "|")
	require.GreaterOrEqual(t, cursor, 0)
	got, ok := CompleteYAMLFile("app.yaml", src[:cursor]+src[cursor+1:], cursor, builtinOnly())
	require.True(t, ok)
	return got
}

func completionLabels(cs []Completion) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Label)
	}
	return out
}

const appHead = "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: demo\nspec:\n"

func TestCompleteApplication(t *testing.T) {
	t.Run("a component's type", func(t *testing.T) {
		got := completeApp(t, appHead+"  components:\n    - name: web\n      type: webs|\n")
		require.Equal(t, []string{"webservice"}, completionLabels(got))
		assert.Equal(t, SourceBuiltin, got[0].Detail)
		assert.NotEmpty(t, got[0].Doc)
		assert.Contains(t, got[0].Snippet, "webservice\n      properties:\n        image: ${1}", "its required parameters, to fill in")
	})
	t.Run("a component that has properties already", func(t *testing.T) {
		got := completeApp(t, appHead+"  components:\n    - name: web\n      type: webs|\n      properties:\n        image: nginx\n")
		require.Len(t, got, 1)
		assert.Empty(t, got[0].Snippet, "the name alone")
	})
	t.Run("a trait's type", func(t *testing.T) {
		labels := completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      type: webservice\n      traits:\n        - type: sca|\n"))
		assert.Contains(t, labels, "scaler")
		assert.NotContains(t, labels, "webservice")
	})
	t.Run("a policy's and a step's type", func(t *testing.T) {
		assert.Contains(t, completionLabels(completeApp(t, appHead+"  policies:\n    - name: p\n      type: topo|\n")), "topology")
		assert.Contains(t, completionLabels(completeApp(t, appHead+"  workflow:\n    steps:\n      - name: s\n        type: depl|\n")), "deploy")
	})
	t.Run("a component's properties", func(t *testing.T) {
		labels := completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      type: webservice\n      properties:\n        ima|\n"))
		assert.Contains(t, labels, "image")
		assert.Contains(t, labels, "imagePullPolicy")
	})
	t.Run("a property the type comes after", func(t *testing.T) {
		labels := completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      properties:\n        ima|\n      type: webservice\n"))
		assert.Contains(t, labels, "image")
	})
	t.Run("a nested property's values", func(t *testing.T) {
		got := completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      type: webservice\n      properties:\n        ports:\n          - port: 80\n            protocol: |\n"))
		assert.Contains(t, got, "TCP")
		assert.Contains(t, got, "UDP")
	})
	t.Run("a trait's properties", func(t *testing.T) {
		assert.Equal(t, []string{"replicas"}, completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      type: webservice\n      traits:\n        - type: scaler\n          properties:\n            rep|\n")))
	})
	t.Run("the Application's own fields", func(t *testing.T) {
		labels := completionLabels(completeApp(t, appHead+"  components:\n    - name: web\n      type: webservice\n      dep|\n"))
		assert.Contains(t, labels, "dependsOn")
	})
}

func TestHoverApplication(t *testing.T) {
	src := appHead + "  components:\n    - name: web\n      type: webservice\n      properties:\n        image: nginx\n"
	h, ok := HoverYAMLFile("app.yaml", src, strings.Index(src, "webservice")+3, builtinOnly())
	require.True(t, ok)
	assert.Contains(t, h, "webservice")
	assert.Contains(t, h, "image")
	assert.Contains(t, h, "required")

	h, ok = HoverYAMLFile("app.yaml", src, strings.Index(src, "image:")+2, builtinOnly())
	require.True(t, ok)
	assert.Contains(t, h, "image: string")
	assert.Contains(t, h, "Which image")
}

// A new Application names its component's type and leaves a tab stop for
// each property the type requires; filled in, it checks clean.
func TestNewApplication(t *testing.T) {
	opts := builtinOnly()
	snippet, ok := NewApplication("shop", "webservice", opts)
	require.True(t, ok)
	assert.Contains(t, snippet, "name: shop")
	assert.Contains(t, snippet, "type: webservice")
	assert.Contains(t, snippet, "image: ${1}")
	filled := regexp.MustCompile(`\$\{\d+(?::[^}]*)?\}|\$0`).ReplaceAllString(snippet, "nginx")
	assert.Empty(t, checkApp(t, filled, opts), filled)

	_, ok = NewApplication("shop", "no-such-type", opts)
	assert.False(t, ok, "a type no source has")

	var names []string
	for _, d := range ComponentTypes(opts) {
		names = append(names, d.Name)
	}
	assert.Contains(t, names, "webservice")
}

// An Application's sources are checked as its components are: the type
// names a source definition, its properties what that takes.
func TestApplicationSources(t *testing.T) {
	opts := builtinOnly()
	app := func(source string) string {
		return "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  sources:\n" + source + "  components:\n    - name: web\n      type: webservice\n      properties:\n        image: nginx\n"
	}
	assert.Empty(t, checkApp(t, app("    - name: cfg\n      type: http-get\n      properties:\n        url: https://example.com\n"), opts), "a source as its type takes it")
	got := checkApp(t, app("    - name: cfg\n      type: http-get\n      properties: {}\n"), opts)
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "url")
	got = checkApp(t, app("    - name: cfg\n      type: no-such-source\n"), opts)
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "no-such-source")
}

// A parameter declared as an empty struct, as k8s-objects' objects: [...{}],
// takes whatever it is given.
func TestEmptyStructParameterTakesAnything(t *testing.T) {
	src := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  components:\n    - name: cfg\n      type: k8s-objects\n      properties:\n        objects:\n          - apiVersion: v1\n            kind: ConfigMap\n            metadata:\n              name: x\n            data:\n              a: b\n"
	assert.Empty(t, checkApp(t, src, builtinOnly()))
	assert.NotEmpty(t, checkApp(t, strings.Replace(src, "        objects:", "        objectz:", 1), builtinOnly()), "a parameter it does not declare is still refused")
}
