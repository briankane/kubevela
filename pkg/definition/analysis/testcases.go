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
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// CheckTestFile checks what the cases of a CUE test file pass their
// definitions: each case's parameter against the parameter of the definition
// it names. The rest of a case is checked when cuetest loads the file.
func CheckTestFile(path string, src []byte, ext *Externals) []Diagnostic {
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return nil
	}
	d := &document{path: path, src: src}
	diags := d.duplicateCases(f)
	for _, decl := range f.Decls {
		c, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		body := caseBodyOf(c.Value)
		if body == nil {
			continue
		}
		declared := map[string]*ast.Field{}
		topLevelFields(body, declared)
		param := declared[parameterLabel]
		if param == nil {
			continue
		}
		definition := stringValue(declared["definition"])
		if definition == "" {
			if def, _, ok := testTarget(string(src), path); ok {
				definition = def
			}
		}
		ctx := cuecontext.New()
		params, ok := definitionParameter(ctx, filepath.Dir(path), definition, ext)
		if !ok {
			continue
		}
		diags = append(diags, d.unknownParameters(param, params, definition, nil)...)
		given := ctx.BuildExpr(param.Value)
		if given.Err() == nil {
			for _, e := range cueerrors.Errors(given.Unify(params).Validate()) {
				diags = append(diags, d.fromErrors(e, "")...)
			}
		}
	}
	return sortDiagnostics(firstPerPosition(diags))
}

// caseBodyOf is the struct a case writes: `test.#X & {...}`, or a struct.
func caseBodyOf(v ast.Expr) *ast.StructLit {
	switch x := v.(type) {
	case *ast.BinaryExpr:
		if x.Op == token.AND {
			if s := caseBodyOf(x.Y); s != nil {
				return s
			}
			return caseBodyOf(x.X)
		}
	case *ast.StructLit:
		return x
	}
	return nil
}

func stringValue(f *ast.Field) string {
	if f == nil {
		return ""
	}
	if lit, ok := f.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		s, _ := literal.Unquote(lit.Value)
		return s
	}
	return ""
}

// definitionParameter is the closed parameter of the definition a test case
// names, by its path relative to the test file, built in ctx.
func definitionParameter(ctx *cue.Context, dir, definition string, ext *Externals) (cue.Value, bool) {
	if definition == "" {
		return cue.Value{}, false
	}
	file := filepath.Join(dir, definition)
	if !strings.HasSuffix(file, ".cue") {
		file += ".cue"
	}
	//nolint:gosec // reading the definition a test case names is the point
	src, err := os.ReadFile(file)
	if err != nil {
		return cue.Value{}, false
	}
	f, err := parser.ParseFile(file, src, parser.ParseComments)
	if err != nil {
		return cue.Value{}, false
	}
	pd, ok := newDocument(file, src, f)
	if !ok {
		return cue.Value{}, false
	}
	pd.opts = Options{Externals: ext}
	v, ok := pd.evaluateIn(ctx)
	if !ok {
		return cue.Value{}, false
	}
	params := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	return params, params.Exists()
}

// frame is a struct open before the cursor: the text that opens it.
type frame struct{ prefix string }

// openFrames are the structs open at the end of text, outermost first,
// outside strings and comments.
func openFrames(text string) []frame {
	var stack []frame
	start, inString := 0, false
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
			start = i
		case c == '{':
			stack = append(stack, frame{prefix: strings.TrimSpace(text[start:i])})
			start = i + 1
		case c == '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			start = i + 1
		case c == '\n' || c == ',':
			start = i + 1
		}
	}
	return stack
}

var (
	testCall   = regexp.MustCompile(`test\.(#[A-Za-z0-9_]+)\s*&$`)
	labelToken = regexp.MustCompile(`("(?:[^"\\]|\\.)*"|[A-Za-z_$#][A-Za-z0-9_$#-]*)\s*[?!]?:`)
	fieldTyped = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)?$`)
)

// labelsOf are the field labels a frame's opening text names, as in
// `expect: output: `.
func labelsOf(prefix string) []string {
	var out []string
	for _, m := range labelToken.FindAllStringSubmatch(prefix, -1) {
		label := m[1]
		if s, err := literal.Unquote(label); err == nil {
			label = s
		}
		out = append(out, label)
	}
	return out
}

// completeInCase completes a field name inside a test case: the test
// function's fields at the path open there, or, under parameter, the
// definition's parameters.
func completeInCase(upToCursor, path, before string, ext *Externals) []Completion {
	m := fieldTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	typed := m[1]
	frames := openFrames(upToCursor[:len(upToCursor)-len(before)])
	call := -1
	var fn string
	for i, f := range frames {
		if c := testCall.FindStringSubmatch(f.prefix); c != nil {
			call, fn = i, c[1]
		}
	}
	if call < 0 {
		return nil
	}
	var fieldPath []string
	for _, f := range frames[call+1:] {
		fieldPath = append(fieldPath, labelsOf(f.prefix)...)
	}
	var v cue.Value
	if len(fieldPath) > 0 && fieldPath[0] == parameterLabel {
		definition := ""
		if m := caseDefinition.FindAllStringSubmatch(upToCursor, -1); len(m) > 0 {
			definition = m[len(m)-1][1]
		} else if def, _, ok := testTarget(upToCursor, path); ok {
			definition = def
		}
		params, ok := definitionParameter(cuecontext.New(), filepath.Dir(path), definition, ext)
		if !ok {
			return nil
		}
		v = params
		fieldPath = fieldPath[1:]
	} else {
		pkg, ok := ext.documented(testPackagePath)
		if !ok {
			return nil
		}
		v = pkg.LookupPath(cue.MakePath(cue.Def(fn)))
	}
	for _, p := range fieldPath {
		v = schemaChild(v, cue.Str(p))
	}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		name := it.Selector().Unquoted()
		if strings.HasPrefix(name, "$") || !strings.HasPrefix(name, typed) {
			continue
		}
		out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: kindName(it.Value()), Doc: docOf(it.Value())})
	}
	return out
}

// docOf is a field's doc comment, without a +usage= marker.
func docOf(v cue.Value) string {
	return usageOf(v)
}

// testAttributes are the attributes a CUE test file may carry.
var testAttributes = []Marker{
	{Name: "label", Doc: "Labels the case, or every case of the file at its top, for `vela def test --label-filter`.", Values: []string{"${1:label}"}},
	{Name: "pending", Doc: "Parks the case, or the file's cases: they load but do not run, and count as pending.", Values: []string{"${1:reason}"}},
	{Name: "upgrade", Doc: "Marks a known dependency on KubeVela's CUE upgrader: a failure as written is retried upgraded, and reported as upgraded.", Values: []string{`reason="${1:why}"`}},
	{Name: "exact", Bare: true, Doc: "On an expected struct: the result may have no fields beyond those expected."},
	{Name: "not", Bare: true, Doc: "On an expected field: passes unless the field is present and matches."},
	{Name: "contains", Bare: true, Doc: "On an expected list: each element must match a different element, in any order."},
	{Name: "before", Bare: true, Doc: "Runs the provider calls of this field once, before the file's cases."},
	{Name: "beforeEach", Bare: true, Doc: "Runs the provider calls of this field before each case."},
	{Name: "afterEach", Bare: true, Doc: "Runs the provider calls of this field after each case."},
	{Name: "after", Bare: true, Doc: "Runs the provider calls of this field once, after the file's cases."},
}

var attributeTyped = regexp.MustCompile(`(?:^|[\s}\])])@([A-Za-z]*)$`)

// CompleteTestAttribute completes an attribute of a CUE test file after `@`.
func CompleteTestAttribute(before string) []Completion {
	if strings.Count(before, `"`)%2 == 1 {
		return nil
	}
	m := attributeTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	var out []Completion
	for _, a := range testAttributes {
		if !strings.HasPrefix(a.Name, m[1]) {
			continue
		}
		snippet := a.Name + "()"
		if !a.Bare {
			snippet = a.Name + "(" + a.Values[0] + ")"
		}
		out = append(out, Completion{Label: "@" + a.Name, Insert: a.Name, Replace: len(m[1]), Doc: a.Doc, Snippet: snippet})
	}
	return out
}

// duplicateCases reports each case declared under a name an earlier case
// has: CUE unifies fields of one name, so the two would merge into one case.
// Helpers (_x) and definitions (#X) are not cases.
func (d *document) duplicateCases(f *ast.File) []Diagnostic {
	first := map[string]*ast.Field{}
	var diags []Diagnostic
	for _, decl := range f.Decls {
		c, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		name := labelName(c.Label)
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "#") {
			continue
		}
		prev, seen := first[name]
		if !seen {
			first[name] = c
			continue
		}
		diags = append(diags, d.at(c.Label.Pos(), fmt.Sprintf("test case %q is also declared on line %d: CUE merges cases of one name into one. Give each case its own name", name, prev.Label.Pos().Line())))
	}
	return diags
}
