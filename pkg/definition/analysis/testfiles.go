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
	candidates = append(candidates, strings.TrimSuffix(filepath.Base(path), "_test.cue"))
	for _, c := range candidates {
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
