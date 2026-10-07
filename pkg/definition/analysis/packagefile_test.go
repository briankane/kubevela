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

// goodPackage is a Package whose lines the cases below count on: the
// template's CUE starts on line 12.
const goodPackage = `apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata:
  name: hello
spec:
  path: ext/hello
  provider:
    protocol: http
    endpoint: http://hello.example.com/
  templates:
    hello/say.cue: |
      package hello

      // +usage=Says hello
      #Say: {
        #do:       "say"
        #provider: "hello"
        $params: {
          name: string
        }
        $returns: message: string
      }
`

// checkPackage reports what CheckPackageFile finds in src as
// "line:col severity: message".
func checkPackage(t *testing.T, src string) []string {
	t.Helper()
	diags, ok := CheckPackageFile("hello-package.yaml", []byte(src), nil)
	require.True(t, ok, "a Package")
	var out []string
	for _, d := range diags {
		sev := map[Severity]string{SeverityError: "error", SeverityWarning: "warning", SeverityInfo: "info"}[d.Severity]
		out = append(out, strconv.Itoa(d.Range.Start.Line)+":"+strconv.Itoa(d.Range.Start.Column)+" "+sev+": "+d.Message)
	}
	return out
}

func TestPackageFile(t *testing.T) {
	cases := map[string]struct {
		edit func(string) string
		want []string
	}{
		"a valid package": {edit: func(s string) string { return s }},
		"a protocol the CRD does not take": {
			edit: func(s string) string { return strings.Replace(s, "protocol: http", "protocol: ftp", 1) },
			want: []string{"8:", "error", "protocol"},
		},
		"a protocol KubeVela cannot call yet": {
			edit: func(s string) string { return strings.Replace(s, "protocol: http", "protocol: grpc", 1) },
			want: []string{"8:", "warning", "grpc"},
		},
		"a path KubeVela keeps for its own": {
			edit: func(s string) string { return strings.Replace(s, "path: ext/hello", "path: vela/hello", 1) },
			want: []string{"6:", "error", "vela/"},
		},
		"a provider key misspelt": {
			edit: func(s string) string { return strings.Replace(s, "endpoint:", "endpont:", 1) },
			want: []string{"9:", "error", "endpont"},
		},
		"a path whose last part is no CUE name, and a package clause": {
			edit: func(s string) string { return strings.Replace(s, "path: ext/hello", "path: ext/my-hello", 1) },
			want: []string{"12:", "error", "my-hello is not a CUE name", "no package clause", "alias"},
		},
		"a path whose last part is no CUE name, and no package clause": {
			edit: func(s string) string {
				s = strings.Replace(s, "path: ext/hello", "path: ext/my-hello", 1)
				return strings.Replace(s, "      package hello\n", "", 1)
			},
		},
		"a template of another package": {
			edit: func(s string) string { return strings.Replace(s, "package hello", "package hi", 1) },
			want: []string{"12:", "error", "hello"},
		},
		"a type error in a template": {
			edit: func(s string) string { return strings.Replace(s, "name: string", "name: string & 1", 1) },
			want: []string{"19:", "error"},
		},
		"a syntax error in a template": {
			edit: func(s string) string {
				return strings.Replace(s, "$returns: message: string", "$returns: message: string }", 1)
			},
			// The extra brace closes #Say, so the stray one is the closing brace
			// on the next line, at its own column in the YAML.
			want: []string{"22:7 error", "expected 'EOF'"},
		},
		"a provider no package is named": {
			edit: func(s string) string { return strings.Replace(s, `#provider: "hello"`, `#provider: "helo"`, 1) },
			want: []string{"17:", "warning", "helo"},
		},
		"a call with no provider to make it": {
			edit: func(s string) string {
				return strings.Replace(s, "  provider:\n    protocol: http\n    endpoint: http://hello.example.com/\n", "", 1)
			},
			want: []string{"error", "spec.provider"},
		},
		"a call with no params": {
			edit: func(s string) string {
				return strings.Replace(s, "        $params: {\n          name: string\n        }\n", "", 1)
			},
			want: []string{"15:", "error", "$params"},
		},
		"no name": {
			edit: func(s string) string { return strings.Replace(s, "metadata:\n  name: hello\n", "metadata: {}\n", 1) },
			want: []string{"error", "name"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkPackage(t, tc.edit(goodPackage))
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

func TestPackageFileAmongOtherDocuments(t *testing.T) {
	src := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n---\n" + strings.Replace(goodPackage, "protocol: http", "protocol: ftp", 1)
	got := checkPackage(t, src)
	require.Len(t, got, 1)
	assert.True(t, strings.HasPrefix(got[0], "13:"), "lines count from the stream: %s", got[0])

	_, ok := CheckPackageFile("cm.yaml", []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), nil)
	assert.False(t, ok, "no Package in it")
}

func TestNewPackageOfAPathWithAHyphen(t *testing.T) {
	src := NewPackage("my-package", "ext/my-package", "")
	diags, ok := CheckPackageFile("my-package-package.yaml", []byte(src), nil)
	require.True(t, ok)
	assert.Empty(t, diags, "clean:\n%s", src)
	assert.NotContains(t, src, "package my-package")
	assert.Contains(t, src, `import my_package "ext/my-package"`, "says how to import it")
}

func TestImportOfAPackageWithNoName(t *testing.T) {
	pkgs, errs := ParsePackages([]byte(NewPackage("my-package", "ext/my-package", "")))
	require.Empty(t, errs)
	require.Len(t, pkgs, 1)
	def := func(imp, ref string) string {
		return imp + "\n\"s\": {\n\ttype: \"workflow-step\"\n\tdescription: \"d\"\n}\ntemplate: {\n\tn: (" + ref + ".#Name & {name: \"a\", suffix: \"b\"}).out\n\tparameter: {}\n}\n"
	}
	opts := Options{Externals: NewExternals(pkgs)}
	errorsOf := func(src string) []Diagnostic {
		var out []Diagnostic
		for _, d := range AnalyzeWith("s.cue", []byte(src), opts).Diagnostics {
			if d.Severity == SeverityError {
				out = append(out, d)
			}
		}
		return out
	}
	assert.Empty(t, errorsOf(def(`import my_package "ext/my-package"`, "my_package")), "imported with an alias")

	got := errorsOf(def(`import "ext/my-package"`, "my_package"))
	require.NotEmpty(t, got)
	assert.Equal(t, 1, got[0].Range.Start.Line)
	assert.Contains(t, got[0].Message, "alias")
	require.Len(t, got[0].Fixes, 1)
	assert.Equal(t, "my_package ", got[0].Fixes[0].Edits[0].NewText)
	assert.Equal(t, 8, got[0].Fixes[0].Edits[0].Range.Start.Column, "before the path")
}

func TestCompleteImportOfAHyphenatedPath(t *testing.T) {
	typed, ok := importPathTyped(`import "ext/my-`)
	require.True(t, ok)
	assert.Equal(t, "ext/my-", typed)
}

func TestNewPackage(t *testing.T) {
	for _, protocol := range []string{"http", "https", ""} {
		src := NewPackage("greeter", "ext/greeter", protocol)
		assert.Contains(t, src, "name: greeter")
		assert.Contains(t, src, "path: ext/greeter")
		diags, ok := CheckPackageFile("greeter-package.yaml", []byte(src), nil)
		require.True(t, ok)
		assert.Empty(t, diags, "the scaffold with protocol %q is clean:\n%s", protocol, src)
		if protocol == "" {
			assert.NotContains(t, src, "provider:", "a package of plain CUE calls nothing")
		}
	}
}
