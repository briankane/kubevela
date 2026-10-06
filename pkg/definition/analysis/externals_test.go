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

const helloPackage = `apiVersion: cue.oam.dev/v1alpha1
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
      #Say: {
        #do:       "say"
        #provider: "hello"
        // +usage=The params of this action
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
apiVersion: v1
kind: ConfigMap
metadata: {name: not-a-package}
`

const usesHello = `import "ext/hello"

` + "\"greeter\": {\n\ttype: \"component\"\n}\n" + `template: {
	_greet: hello.#Say & {$params: name: parameter.name}
	output: {apiVersion: "v1", kind: "ConfigMap", data: msg: _greet.$returns.message}
	parameter: {
		// +usage=Who to greet
		name: string
	}
}
`

func helloExternals(t *testing.T) *Externals {
	t.Helper()
	pkgs, errs := ParsePackages([]byte(helloPackage))
	require.Empty(t, errs)
	require.Len(t, pkgs, 1, "the ConfigMap is not a package")
	return NewExternals(pkgs)
}

func TestCustomProviderImports(t *testing.T) {
	ext := helloExternals(t)
	for _, d := range AnalyzeWith("def.cue", []byte(usesHello), Options{Externals: ext}).Diagnostics {
		assert.NotEqual(t, SeverityError, d.Severity, d.Message)
	}

	without := lines(Analyze("def.cue", []byte(usesHello)).Diagnostics)
	require.NotEmpty(t, without)
	assert.Contains(t, strings.Join(without, "\n"), "ext/hello")

	misspelt := strings.Replace(usesHello, "hello.#Say", "hello.#Sya", 1)
	got := lines(AnalyzeWith("def.cue", []byte(misspelt), Options{Externals: ext}).Diagnostics)
	assert.Contains(t, strings.Join(got, "\n"), "package ext/hello has no member #Sya")
}

func TestCustomProviderCompletion(t *testing.T) {
	ext := helloExternals(t)
	members := CompletePackageMemberWith(usesHello, "\t_greet: hello.#", ext)
	require.Equal(t, []string{"#Say"}, labels(members))
	assert.Contains(t, members[0].Detail, "name")
	assert.Contains(t, members[0].Doc, "Who to greet")

	imports := CompleteImportWith(usesHello, `import "ext/`, ext)
	assert.Equal(t, []string{"ext/hello"}, labels(imports))
}

func TestParsePackagesReportsABrokenOne(t *testing.T) {
	_, errs := ParsePackages([]byte("apiVersion: cue.oam.dev/v1alpha1\nkind: Package\nmetadata: {name: bad}\nspec:\n  path: ext/bad\n  templates:\n    bad.cue: \"#X: {\"\n"))
	assert.NotEmpty(t, errs)
}

// A custom provider runs on every render of a component or trait, so its
// use there warns; a workflow step runs it once, so does not.
func TestCustomProvidersInRendersWarn(t *testing.T) {
	ext := helloExternals(t)
	warnings := func(src string) []Diagnostic {
		var out []Diagnostic
		for _, d := range AnalyzeWith("def.cue", []byte(src), Options{Externals: ext}).Diagnostics {
			if d.Severity == SeverityWarning {
				out = append(out, d)
			}
		}
		return out
	}
	got := warnings(usesHello)
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Range.Start.Line)
	assert.Contains(t, got[0].Message, "experimental")
	assert.Contains(t, got[0].Message, "deterministic")

	step := strings.Replace(usesHello, `type: "component"`, `type: "workflow-step"`, 1)
	assert.Empty(t, warnings(step))

	builtin := "import \"vela/kube\"\n\n\"c\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\t_r: kube.#Read & {$params: resource: {apiVersion: \"v1\", kind: \"ConfigMap\"}}\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n}\n"
	assert.Empty(t, warnings(builtin), "KubeVela's own packages are not custom providers")
}
