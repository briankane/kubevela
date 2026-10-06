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
	"os"
	"path/filepath"
	"strings"
	"testing"

	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPackage stands in for cuetest's vela/test, which this package cannot
// import: the functions and fields that matter to completion.
const testPackageSource = `package test
_#case: {
	definition: string
	parameter?: {...}
	...
}
#ComponentRender: {
	_#case
	$test: "component-render"
	expect: {...}
}
// #TraitRender renders a trait definition against a workload it patches.
#TraitRender: {
	_#case
	$test: "trait-render"
	// workload is the object the trait patches.
	workload: {...}
	expect: {...}
}
#ComponentStatus: {
	_#case
	$test: "component-status"
	expect: {...}
}
#TraitStatus: {
	_#case
	$test: "trait-status"
	workload: {...}
	expect: {...}
}
`

func testExternals(t *testing.T) *Externals {
	t.Helper()
	pkg, err := cuexruntime.NewInternalPackage("test", testPackageSource, nil)
	require.NoError(t, err)
	return NewExternals([]cuexruntime.Package{pkg})
}

// testDir is a folder with a trait and a component, beside which test files
// are written.
func testDir(t *testing.T) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scaler.cue"), []byte(traitHeader+"template: patch: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web.cue"), []byte(componentHeader+"template: output: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n"), 0o600))
	return dir
}

func TestTestFileFunctionsFollowTheDefinition(t *testing.T) {
	dir, ext := testDir(t), testExternals(t)
	doc := "import \"vela/test\"\n\n\"patches\": test.#"
	cs := CompleteTestFile(doc, filepath.Join(dir, "scaler_test.cue"), "\"patches\": test.#", ext)
	assert.ElementsMatch(t, []string{"#TraitRender", "#TraitStatus"}, labels(cs), "a trait's test file is offered trait tests")
	for _, c := range cs {
		if c.Label == "#TraitRender" {
			assert.Contains(t, c.Snippet, `definition: "scaler"`, "the definition is filled in")
			assert.Contains(t, c.Snippet, "workload: ${")
			assert.Contains(t, c.Doc, "renders a trait definition against a workload it patches")
		}
	}

	// The definition a case names decides it, whatever the file is called.
	named := "import \"vela/test\"\n\n\"a\": test.#ComponentRender & {definition: \"web\"}\n\"b\": test.#"
	assert.ElementsMatch(t, []string{"#ComponentRender", "#ComponentStatus"}, labels(CompleteTestFile(named, filepath.Join(dir, "cases_test.cue"), "\"b\": test.#", ext)))
}

func TestTestFileNewCases(t *testing.T) {
	dir, ext := testDir(t), testExternals(t)
	doc := "import \"vela/test\"\n\n"
	cs := CompleteTestFile(doc, filepath.Join(dir, "scaler_test.cue"), "", ext)
	require.ElementsMatch(t, []string{"New #TraitRender case for scaler", "New #TraitStatus case for scaler"}, labels(cs))
	render := cs[0]
	if !strings.Contains(render.Label, "Render") {
		render = cs[1]
	}
	assert.True(t, strings.HasPrefix(render.Snippet, `"${1:`), render.Snippet)
	assert.Contains(t, render.Snippet, `test.#TraitRender & {`)
	assert.Contains(t, render.Snippet, `definition: "scaler"`)

	assert.Empty(t, CompleteTestFile("import \"vela/test\"\n\n\"x\": test.#TraitRender & {\n\t", filepath.Join(dir, "scaler_test.cue"), "\t", ext), "not inside a case")
}

func TestTestFilesCompleteProviderFunctions(t *testing.T) {
	doc := "import (\n\t\"vela/test\"\n\t\"vela/kube\"\n)\n\n_setup: kube.#"
	cs := CompletePackageMemberWith(doc, "_setup: kube.#", testExternals(t))
	assert.Contains(t, labels(cs), "#Apply", "a test file compiles with the workflow's providers")
}
