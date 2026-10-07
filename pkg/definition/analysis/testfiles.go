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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cuelang.org/go/cue"

	"github.com/oam-dev/kubevela/pkg/definition/goloader"
)

// testPackagePath is the package a CUE test file imports its functions from.
const testPackagePath = "vela/test"

var (
	testImport      = regexp.MustCompile(`"vela/test"`)
	caseDefinition  = regexp.MustCompile(`\bdefinition:\s*"([^"]+)"`)
	testMemberTyped = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.$#])test\.(#?[A-Za-z0-9_]*)$`)
)

// isTestDoc reports whether doc is a CUE test file: it imports vela/test.
func isTestDoc(doc string) bool {
	return testImport.MatchString(doc)
}

// testsFor are the test functions for each definition type, render first.
var testsFor = map[string][]string{
	componentType:    {"#ComponentRender", "#ComponentStatus"},
	traitType:        {"#TraitRender", "#TraitStatus"},
	policyType:       {"#PolicyRender", "#ApplicationPolicyRender"},
	workflowStepType: {"#WorkflowStepExec"},
	sourceType:       {"#SourceExec"},
}

// testTarget is the definition a test file tests: the one its cases name,
// or the file it is named after. It is the definition's path as a case
// writes it, and its type.
func testTarget(doc, path string) (definition, defType string, ok bool) {
	dir := filepath.Dir(path)
	var candidates []string
	if m := caseDefinition.FindStringSubmatch(doc); m != nil {
		candidates = append(candidates, m[1])
	}
	base := strings.TrimSuffix(filepath.Base(path), "_test.cue")
	candidates = append(candidates, base, base+".go")
	for _, c := range candidates {
		if rel, name, _ := strings.Cut(c, "#"); strings.HasSuffix(rel, ".go") {
			if typ, ok := defkitType(filepath.Join(dir, rel), name); ok {
				return c, typ, true
			}
			continue
		}
		file := filepath.Join(dir, c)
		if !strings.HasSuffix(file, ".cue") {
			file += ".cue"
		}
		//nolint:gosec // reading the definition a test file names is the point
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if t := headerType.FindStringSubmatch(string(src)); t != nil {
			return strings.TrimSuffix(c, ".cue"), kindFromText(string(src), t[1]), true
		}
	}
	return "", "", false
}

// defkitType is the type of the DefKit definition named in a Go file, or of
// its only one, read from the Go without rendering it.
func defkitType(file, name string) (string, bool) {
	defs, err := goloader.AnalyzeGoFile(file)
	if err != nil {
		return "", false
	}
	for _, d := range defs {
		if d.Name == name || (name == "" && len(defs) == 1) {
			return d.Type, true
		}
	}
	return "", false
}

// CompleteTestFile completes in a CUE test file at path, given its text up
// to the cursor and the line of it: inside a case, the test function's
// fields or the definition's parameters; after `test.`, the
// test functions for the type of the definition it tests, each inserting a
// case with that definition filled in; on an empty line between cases, a new
// case of each.
func CompleteTestFile(doc, path, before string, ext *Externals) []Completion {
	if !isTestDoc(doc) {
		return nil
	}
	definition, defType, ok := testTarget(doc, path)
	if !ok {
		return nil
	}
	if defType == applicationPolicy {
		defType = policyType
	}
	tests := testsFor[defType]
	pkg, ok := ext.value(testPackagePath)
	if !ok {
		return nil
	}
	if inside := completeInCase(doc, path, before, ext); inside != nil {
		return inside
	}
	if m := testMemberTyped.FindStringSubmatch(before); m != nil {
		var out []Completion
		for _, fn := range tests {
			if !strings.HasPrefix(fn, m[1]) {
				continue
			}
			v := pkg.LookupPath(cue.MakePath(cue.Def(fn)))
			out = append(out, Completion{Label: fn, Insert: fn, Replace: len(m[1]), Doc: testDoc(ext, fn), Snippet: fn + " & {\n" + caseBody(v, definition, 1) + "}"})
		}
		return out
	}
	if strings.TrimSpace(before) != "" || depth(doc[:len(doc)-len(before)]) > 0 {
		return nil
	}
	var out []Completion
	for _, fn := range tests {
		v := pkg.LookupPath(cue.MakePath(cue.Def(fn)))
		out = append(out, Completion{
			Label:   fmt.Sprintf("New %s case for %s", fn, definition),
			Insert:  fn,
			Doc:     testDoc(ext, fn),
			Snippet: `"${1:what this case checks}": test.` + fn + " & {\n" + caseBody(v, definition, 2) + "}\n",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// caseBody is a case's required fields, the definition filled in and each
// other one a tab stop numbered from first.
func caseBody(fn cue.Value, definition string, first int) string {
	var b strings.Builder
	n := first
	it, err := fn.Fields()
	if err != nil {
		return ""
	}
	for it.Next() {
		name := it.Selector().Unquoted()
		switch {
		case strings.HasPrefix(name, "$"):
			continue
		case name == "definition":
			fmt.Fprintf(&b, "\tdefinition: %q\n", definition)
			continue
		}
		fmt.Fprintf(&b, "\t%s: ${%d}\n", snippetEscape(name), n)
		n++
	}
	return b.String()
}

// testDoc is a test function's doc comment.
func testDoc(ext *Externals, fn string) string {
	v, ok := ext.documented(testPackagePath)
	if !ok {
		return ""
	}
	var lines []string
	for _, cg := range v.LookupPath(cue.MakePath(cue.Def(fn))).Doc() {
		lines = append(lines, strings.TrimSpace(cg.Text()))
	}
	return strings.Join(lines, "\n")
}

// depth is how many braces are open at the end of text, outside strings and
// comments.
func depth(text string) int {
	n, inString := 0, false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\\' && inString:
			i++
		case c == '"':
			inString = !inString
		case inString:
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '{':
			n++
		case c == '}':
			n--
		}
	}
	return n
}

// TestKind is a kind of test a definition can have: the test function, and
// what it checks.
type TestKind struct {
	Function string
	Label    string
	Doc      string
}

// testKindLabels say what each test function checks.
var testKindLabels = map[string]string{
	"#ComponentRender":         "Render: what it outputs for given parameters",
	"#ComponentStatus":         "Status: its health and message for a live object",
	"#TraitRender":             "Render: what it patches and outputs, for a workload",
	"#TraitStatus":             "Status: its health and message for a live object",
	"#PolicyRender":            "Render: what it outputs",
	"#ApplicationPolicyRender": "Render: what it does to an Application",
	"#WorkflowStepExec":        "Exec: run it as the workflow engine does",
	"#SourceExec":              "Exec: resolve it as the controller does",
}

// testFunctionsOf are the test functions for a definition of type defType,
// whose template kind is kind: an Application-scoped policy has its own.
func testFunctionsOf(defType, kind string) []string {
	switch {
	case kind == applicationPolicy:
		return []string{"#ApplicationPolicyRender"}
	case defType == policyType:
		return []string{"#PolicyRender"}
	}
	return testsFor[defType]
}

// TestKinds are the kinds of test the definitions at path can have, by
// their type: src is a CUE definition's text; a DefKit Go file is read from
// disk.
func TestKinds(path string, src []byte, ext *Externals) []TestKind {
	var fns []string
	if strings.HasSuffix(path, ".go") {
		defs, err := goloader.AnalyzeGoFile(path)
		if err != nil {
			return nil
		}
		seen := map[string]bool{}
		for _, d := range defs {
			for _, fn := range testFunctionsOf(d.Type, d.Type) {
				if !seen[fn] {
					seen[fn] = true
					fns = append(fns, fn)
				}
			}
		}
	} else {
		_, defType, ok := DefinitionHeader(path, src)
		if !ok {
			return nil
		}
		fns = testFunctionsOf(defType, kindFromText(string(src), defType))
	}
	out := make([]TestKind, 0, len(fns))
	for _, fn := range fns {
		out = append(out, TestKind{Function: fn, Label: testKindLabels[fn], Doc: testDoc(ext, fn)})
	}
	return out
}

// DefinitionOfTest is the definition file a test file tests: the one its
// cases name, or the one it is named after.
func DefinitionOfTest(path, doc string) (string, bool) {
	ref, _, ok := testTarget(doc, path)
	if !ok {
		return "", false
	}
	ref, _, _ = strings.Cut(ref, "#")
	file := filepath.Join(filepath.Dir(path), ref)
	if !strings.HasSuffix(file, ".go") {
		file += ".cue"
	}
	if _, err := os.Stat(file); err != nil {
		return "", false
	}
	return file, true
}

// NewTestFile is NewTestFileOf for each definition's first kind of test.
func NewTestFile(path string, src []byte, ext *Externals) (string, string, bool) {
	file, snippet, _, ok := NewTestFileOf(path, src, ext, "")
	return file, snippet, ok
}

// NewTestFileOf is the test file to create for the definitions at path:
// its path beside it, its text as a snippet, and the cases alone, to add to
// a test file that exists. Each definition gets a case calling fn, or its
// type's first test where fn is empty, with its fields to fill in. src is a CUE
// definition's text; a DefKit Go file is read from disk. It is false for a
// file that defines nothing testable, or is a test file.
func NewTestFileOf(path string, src []byte, ext *Externals, fn string) (file, snippet, cases string, ok bool) {
	if strings.HasSuffix(path, "_test.cue") || isTestDoc(string(src)) {
		return "", "", "", false
	}
	pkg, ok := ext.documented(testPackagePath)
	if !ok {
		return "", "", "", false
	}
	type target struct{ ref, typ, kind, name string }
	var targets []target
	base := filepath.Base(path)
	if strings.HasSuffix(path, ".go") {
		defs, err := goloader.AnalyzeGoFile(path)
		if err != nil {
			return "", "", "", false
		}
		for _, d := range defs {
			ref := base
			if len(defs) > 1 {
				ref += "#" + d.Name
			}
			targets = append(targets, target{ref: ref, typ: d.Type, kind: d.Type, name: d.Name})
		}
	} else {
		name, defType, ok := DefinitionHeader(path, src)
		if !ok {
			return "", "", "", false
		}
		targets = append(targets, target{ref: strings.TrimSuffix(base, ".cue"), typ: defType, kind: kindFromText(string(src), defType), name: name})
	}
	var b strings.Builder
	n := 1
	for _, t := range targets {
		fns := testFunctionsOf(t.typ, t.kind)
		use := ""
		for _, f := range fns {
			if f == fn || fn == "" && use == "" {
				use = f
			}
		}
		if use == "" {
			continue
		}
		body := pkg.LookupPath(cue.MakePath(cue.Def(use)))
		what := "renders with its defaults"
		if strings.HasSuffix(use, "Status") {
			what = "is healthy"
		} else if strings.HasSuffix(use, "Exec") {
			what = "runs"
		}
		fmt.Fprintf(&b, "\n\"${%d:%s %s}\": test.%s & {\n", n, t.name, what, use)
		b.WriteString(caseBody(body, t.ref, n+1))
		b.WriteString("}\n")
		n += 1 + strings.Count(caseBody(body, t.ref, 0), "${")
	}
	if n == 1 {
		return "", "", "", false
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(base, ".cue"), ".go")
	cases = b.String()
	return filepath.Join(filepath.Dir(path), stem+"_test.cue"), fmt.Sprintf("import \"%s\"\n", testPackagePath) + cases, cases, true
}
