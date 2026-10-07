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
	"strconv"
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

	for _, c := range CompleteTestFile("import \"vela/test\"\n\n\"x\": test.#TraitRender & {\n\t", filepath.Join(dir, "scaler_test.cue"), "\t", ext) {
		assert.NotContains(t, c.Label, "New ", "inside a case, its fields are offered, not new cases")
	}
}

func TestTestFilesCompleteProviderFunctions(t *testing.T) {
	doc := "import (\n\t\"vela/test\"\n\t\"vela/kube\"\n)\n\n_setup: kube.#"
	cs := CompletePackageMemberWith(doc, "_setup: kube.#", testExternals(t))
	assert.Contains(t, labels(cs), "#Apply", "a test file compiles with the workflow's providers")
}

// scalerWithParams is a trait whose parameters a test case passes.
const scalerWithParams = `"scaler": {
	type: "trait"
}
template: {
	patch: spec: replicas: parameter.replicas
	parameter: {
		// +usage=How many replicas
		replicas: *1 | int
		// +usage=Pause the rollout
		paused?: bool
	}
}
`

func paramDir(t *testing.T) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scaler.cue"), []byte(scalerWithParams), 0o600))
	return dir
}

func TestTestCaseParameters(t *testing.T) {
	dir, ext := paramDir(t), testExternals(t)
	check := func(params string) []string {
		src := "import \"vela/test\"\n\n\"c\": test.#TraitRender & {\n\tdefinition: \"scaler\"\n\tworkload: {}\n\tparameter: " + params + "\n\texpect: {}\n}\n"
		return lines(CheckTestFile(filepath.Join(dir, "scaler_test.cue"), []byte(src), ext))
	}
	assert.Empty(t, check("{replicas: 2, paused: true}"))
	got := check("{replicaz: 2}")
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "6: scaler takes no parameter replicaz")
	got = check(`{replicas: "two"}`)
	require.NotEmpty(t, got)
	assert.Contains(t, strings.Join(got, "\n"), "6: ")
	assert.Contains(t, strings.Join(got, "\n"), "replicas")
}

func TestCompleteInsideATestCase(t *testing.T) {
	dir, ext := paramDir(t), testExternals(t)
	path := filepath.Join(dir, "scaler_test.cue")
	open := "import \"vela/test\"\n\n\"c\": test.#TraitRender & {\n\tdefinition: \"scaler\"\n\t"
	t.Run("the test function's fields", func(t *testing.T) {
		got := labels(CompleteTestFile(open, path, "\t", ext))
		assert.Contains(t, got, "workload")
		assert.Contains(t, got, "expect")
		assert.Contains(t, got, "parameter")
		assert.NotContains(t, got, "$test")
	})
	t.Run("the definition's parameters", func(t *testing.T) {
		doc := open + "parameter: {\n\t\t"
		cs := CompleteTestFile(doc, path, "\t\t", ext)
		assert.ElementsMatch(t, []string{"paused", "replicas"}, labels(cs))
		for _, c := range cs {
			if c.Label == "replicas" {
				assert.Equal(t, "How many replicas", c.Doc)
			}
		}
	})
}

func TestCompleteTestAttributes(t *testing.T) {
	cs := CompleteTestAttribute("\"c\": test.#TraitRender & {} @")
	assert.Contains(t, labels(cs), "@pending")
	assert.Contains(t, labels(cs), "@label")
	assert.Contains(t, labels(cs), "@exact")
	assert.Contains(t, labels(cs), "@beforeEach")
	for _, c := range cs {
		assert.NotEmpty(t, c.Doc, c.Label)
	}
	assert.Equal(t, []string{"@pending"}, labels(CompleteTestAttribute("} @pen")))
	assert.Empty(t, CompleteTestAttribute("x: \"a@b"))
}

func TestNewTestFile(t *testing.T) {
	ext := testExternals(t)
	dir := t.TempDir()
	def := filepath.Join(dir, "scaler.cue")
	path, snippet, ok := NewTestFile(def, []byte(scalerWithParams), ext)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "scaler_test.cue"), path)
	assert.Contains(t, snippet, `import "vela/test"`)
	assert.Contains(t, snippet, `test.#TraitRender & {`)
	assert.Contains(t, snippet, `definition: "scaler"`)
	assert.Contains(t, snippet, "${", "it is a snippet to fill in")

	_, _, ok = NewTestFile(def, []byte("x: 1\n"), ext)
	assert.False(t, ok, "not a definition")
	_, _, ok = NewTestFile(filepath.Join(dir, "scaler_test.cue"), []byte(scalerWithParams), ext)
	assert.False(t, ok, "a test file has no test file")
}

const defkitTraits = `package traits

import "github.com/oam-dev/kubevela/pkg/definition/defkit"

func Scaler() *defkit.TraitDefinition { return nil }

func Labels() *defkit.TraitDefinition { return nil }
`

// A DefKit file's test names its definitions by the Go file, rendered when
// the test runs; one file may define several.
func TestNewTestFileForDefKit(t *testing.T) {
	ext := testExternals(t)
	dir := t.TempDir()
	goFile := filepath.Join(dir, "traits.go")
	require.NoError(t, os.WriteFile(goFile, []byte(defkitTraits), 0o600))
	path, snippet, ok := NewTestFile(goFile, []byte(defkitTraits), ext)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "traits_test.cue"), path)
	assert.Contains(t, snippet, `definition: "traits.go#scaler"`)
	assert.Contains(t, snippet, `definition: "traits.go#labels"`)
	assert.Equal(t, 2, strings.Count(snippet, "test.#TraitRender & {"))

	one := strings.Replace(defkitTraits, "func Labels() *defkit.TraitDefinition { return nil }\n", "", 1)
	require.NoError(t, os.WriteFile(goFile, []byte(one), 0o600))
	_, snippet, ok = NewTestFile(goFile, nil, ext)
	require.True(t, ok)
	assert.Contains(t, snippet, `definition: "traits.go"`, "a file's only definition needs no name")

	// The test file it makes is then helped as any other: its cases are
	// trait tests.
	require.NoError(t, os.WriteFile(goFile, []byte(defkitTraits), 0o600))
	doc := "import \"vela/test\"\n\n\"c\": test.#TraitRender & {\n\tdefinition: \"traits.go#scaler\"\n}\n\"d\": test.#"
	var labels []string
	for _, c := range CompleteTestFile(doc, filepath.Join(dir, "traits_test.cue"), "\"d\": test.#", ext) {
		labels = append(labels, c.Label)
	}
	assert.Contains(t, labels, "#TraitRender")
	assert.NotContains(t, labels, "#ComponentRender")
}

// The kinds of test a definition can have follow its type.
func TestTestKinds(t *testing.T) {
	ext := testExternals(t)
	kinds := func(src string) []string {
		var out []string
		for _, k := range TestKinds("d.cue", []byte(src), ext) {
			assert.NotEmpty(t, k.Label, k.Function)
			out = append(out, k.Function)
		}
		return out
	}
	assert.Equal(t, []string{"#TraitRender", "#TraitStatus"}, kinds(scalerWithParams))
	step := "\"s\": {\n\ttype: \"workflow-step\"\n}\ntemplate: parameter: {}\n"
	assert.Equal(t, []string{"#WorkflowStepExec"}, kinds(step))
	appPolicy := "\"p\": {\n\ttype: \"policy\"\n\tattributes: scope: \"Application\"\n}\ntemplate: {\n\toutput: {}\n\tparameter: {}\n}\n"
	assert.Equal(t, []string{"#ApplicationPolicyRender"}, kinds(appPolicy))
	assert.Empty(t, kinds("x: 1\n"))
}

// A test of a chosen kind: the file to create, and the case alone, to add
// to one that exists.
func TestNewTestFileOfAKind(t *testing.T) {
	ext := testExternals(t)
	dir := t.TempDir()
	def := filepath.Join(dir, "scaler.cue")
	path, snippet, cases, ok := NewTestFileOf(def, []byte(scalerWithParams), ext, "#TraitStatus")
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "scaler_test.cue"), path)
	assert.Contains(t, snippet, `import "vela/test"`)
	assert.Contains(t, snippet, "test.#TraitStatus & {")
	assert.NotContains(t, cases, "import", "the case alone")
	assert.Contains(t, cases, "test.#TraitStatus & {")

	_, _, _, ok = NewTestFileOf(def, []byte(scalerWithParams), ext, "#WorkflowStepExec")
	assert.False(t, ok, "a kind the type has not")
}

// A test file leads to the definition it tests.
func TestDefinitionOfTest(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "scaler.cue")
	require.NoError(t, os.WriteFile(def, []byte(scalerWithParams), 0o600))
	test := filepath.Join(dir, "scaler_test.cue")
	got, ok := DefinitionOfTest(test, "import \"vela/test\"\n\nx: test.#TraitRender & {definition: \"scaler\"}\n")
	require.True(t, ok)
	assert.Equal(t, def, got)
	_, ok = DefinitionOfTest(filepath.Join(dir, "other_test.cue"), "import \"vela/test\"\n")
	assert.False(t, ok)
}

// Two cases of one name merge, as CUE unifies fields of a name: a test file
// names each case once.
func TestTestCaseNamesAreUnique(t *testing.T) {
	src := `import "vela/test"

_base: {definition: "scaler"}

"scales": test.#TraitRender & _base & {
	parameter: replicas: 2
}

"defaults": test.#TraitRender & _base

"scales": test.#TraitRender & _base & {
	parameter: replicas: 3
}

_base: {}
`
	var got []string
	for _, d := range CheckTestFile("scaler_test.cue", []byte(src), testExternals(t)) {
		got = append(got, strconv.Itoa(d.Range.Start.Line)+" "+d.Message)
	}
	require.Len(t, got, 1, "%v", got)
	assert.Contains(t, got[0], "11 ")
	assert.Contains(t, got[0], `"scales"`)
	assert.Contains(t, got[0], "line 5")
}
