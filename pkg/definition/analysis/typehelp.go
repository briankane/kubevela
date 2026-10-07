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
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// valueTypedAt matches a field's value being written: the label, and what of
// the value is typed so far.
var valueTypedAt = regexp.MustCompile(`(?:^|[\s{,])(?:[A-Za-z_$#][A-Za-z0-9_$#-]*|"[^"]*")[?!]?:\s*([A-Za-z0-9_#*."]*)$`)

// listStep is a list's element in a path.
const listStep = "[]"

// cueTypes offered where a field declares a type: label, snippet, and what it
// means.
var typeItems = []struct{ label, snippet, doc string }{
	{"string", "string", "Text."},
	{"int", "int", "A whole number."},
	{"bool", "bool", "true or false."},
	{"number", "number", "Any number, whole or not."},
	{"float", "float", "A number with a fraction."},
	{"*default | type", "*${1:\"value\"} | ${2:string}", "A value with a default, used when none is given."},
	{`"a" | "b"`, `"${1:a}" | "${2:b}"`, "One of several values."},
	{"int & >=0", "int & >=${1:0}", "A number within bounds."},
	{`string & =~"pattern"`, `string & =~"${1:^[a-z0-9-]+\$}"`, "Text matching a regular expression."},
	{"[...string]", "[...${1:string}]", "A list."},
	{"[...{...}]", "[...{\n\t$0\n}]", "A list of structs."},
	{"{...}", "{\n\t$0\n}", "A struct of fields."},
	{"[string]: string", "[string]: ${1:string}", "A map from names to values."},
}

// CompleteFieldValue completes the value of the field being written at
// cursor in doc: where it declares a type (a parameter, a #definition, a
// _helper), CUE's types and the file's definitions; where its type is known
// (an output's field by its kind's schema, a call's input by its function),
// the values that type takes and the parameter and context references of a
// matching kind.
func CompleteFieldValue(path, doc string, cursor int, opts Options) []Completion {
	lineStart := strings.LastIndex(doc[:cursor], "\n") + 1
	m := valueTypedAt.FindStringSubmatchIndex(doc[lineStart:cursor])
	if m == nil {
		return nil
	}
	typed := doc[lineStart+m[2] : cursor]
	// A reference being written is completed by its members, all of them.
	if strings.Contains(typed, ".") {
		return nil
	}
	start := cursor - len(typed)
	patched := doc[:start] + "_ @" + cursorPlaceholder + "()" + doc[cursor:]
	f, err := parser.ParseFile(path, patched, parser.ParseComments)
	if err != nil {
		return nil
	}
	at, ok := cursorField(f)
	if !ok || len(at.path) < 2 || at.path[0] != templateLabel {
		return nil
	}
	if declaresType(at.path) {
		return cueTypeCompletions(f, typed)
	}
	d, ok := newDocument(path, []byte(patched), f)
	if !ok {
		return nil
	}
	d.opts = opts
	v, ok := d.evaluateForCompletion()
	if !ok {
		return nil
	}
	want, ok := d.expected(v, at)
	if !ok {
		return nil
	}
	var out []Completion
	out = append(out, schemaValues(want)...)
	params := v.LookupPath(cue.MakePath(cue.Str(templateLabel), cue.Str(parameterLabel)))
	out = append(out, referencesOfKind(params, parameterLabel, want.IncompleteKind(), 0)...)
	out = append(out, contextOfKind(d.typ, want.IncompleteKind())...)
	return filterTyped(out, typed)
}

// cursorAt is the field the cursor's value is written in: its path from the
// file's root, list elements as listStep, and the call it is an input of.
type cursorAt struct {
	path []string
	// call is the function a call's struct calls, and callPath the path of
	// the field under that struct.
	call     *ast.SelectorExpr
	callPath []string
}

// cursorField finds the field carrying the cursor's attribute in f.
func cursorField(f *ast.File) (cursorAt, bool) {
	var stack []ast.Node
	var found []ast.Node
	ast.Walk(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		stack = append(stack, n)
		if fd, ok := n.(*ast.Field); ok {
			for _, a := range fd.Attrs {
				if strings.HasPrefix(a.Text, "@"+cursorPlaceholder) {
					found = append([]ast.Node{}, stack...)
					return false
				}
			}
		}
		return true
	}, func(ast.Node) { stack = stack[:len(stack)-1] })
	if found == nil {
		return cursorAt{}, false
	}
	var at cursorAt
	for i, n := range found {
		switch x := n.(type) {
		case *ast.Field:
			at.path = append(at.path, labelName(x.Label))
			if at.call != nil {
				at.callPath = append(at.callPath, labelName(x.Label))
			}
		case *ast.ListLit:
			at.path = append(at.path, listStep)
			if at.call != nil {
				at.callPath = append(at.callPath, listStep)
			}
		case *ast.BinaryExpr:
			if sel, ok := x.X.(*ast.SelectorExpr); ok && x.Op.String() == "&" && i+1 < len(found) && found[i+1] == x.Y {
				at.call, at.callPath = sel, nil
			}
		}
	}
	return at, true
}

// declaresType reports whether a field at path declares a type: in the
// parameter, or in a #definition or _helper.
func declaresType(path []string) bool {
	if len(path) > 1 && path[1] == parameterLabel {
		return true
	}
	for _, p := range path[1:] {
		if strings.HasPrefix(p, "#") || strings.HasPrefix(p, "_") {
			return true
		}
	}
	return false
}

// cueTypeCompletions are CUE's types, and the definitions f declares.
func cueTypeCompletions(f *ast.File, typed string) []Completion {
	var out []Completion
	for _, t := range typeItems {
		out = append(out, Completion{Label: t.label, Insert: t.label, Replace: len(typed), Detail: "type", Doc: t.doc, Snippet: t.snippet})
	}
	seen := map[string]bool{}
	ast.Walk(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.Field); ok {
			if name := labelName(fd.Label); strings.HasPrefix(name, "#") && !seen[name] {
				seen[name] = true
				out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: "definition", Doc: "The " + name + " definition, declared in this file."})
			}
		}
		return true
	}, nil)
	return filterTyped(out, typed)
}

// expected is the schema of the field at: an input of the call it is in, or
// a field of an output whose kind is known.
func (d *document) expected(v cue.Value, at cursorAt) (cue.Value, bool) {
	if at.call != nil {
		root, chain := flatten(at.call)
		if root == nil || len(chain) != 1 {
			return cue.Value{}, false
		}
		path := d.importNames()[root.Name]
		pkg, ok := d.packages().value(path)
		if !ok {
			return cue.Value{}, false
		}
		want := schemaPath(pkg.LookupPath(cue.MakePath(cue.Def(chain[0].Name))), at.callPath)
		return want, want.Exists()
	}
	var objPath, rest []string
	switch {
	case len(at.path) > 2 && at.path[1] == "output":
		objPath, rest = at.path[:2], at.path[2:]
	case len(at.path) > 3 && at.path[1] == "outputs":
		objPath, rest = at.path[:3], at.path[3:]
	default:
		return cue.Value{}, false
	}
	obj := v.LookupPath(cue.MakePath(selectors(objPath)...))
	apiVersion, err1 := obj.LookupPath(cue.ParsePath("apiVersion")).String()
	kind, err2 := obj.LookupPath(cue.ParsePath("kind")).String()
	if err1 != nil || err2 != nil {
		return cue.Value{}, false
	}
	kinds := d.opts.Kinds
	if kinds == nil {
		kinds = bundledKinds()
	}
	gvk := kubeschema.ParseGVK(apiVersion, kind)
	src, ok := kinds.CUE(gvk)
	if !ok {
		return cue.Value{}, false
	}
	schema := v.Context().CompileString(src).LookupPath(cue.ParsePath(kubeschema.Root(gvk)))
	want := schemaPath(schema, rest)
	return want, want.Exists()
}

// schemaPath is the schema at path under v, a list's element at listStep.
func schemaPath(v cue.Value, path []string) cue.Value {
	for _, p := range path {
		if !v.Exists() {
			return v
		}
		if p == listStep {
			v = v.LookupPath(cue.MakePath(cue.AnyIndex))
			continue
		}
		v = schemaChild(v, cue.Str(p))
	}
	return v
}

// schemaValues are the values a schema takes, when it names them: each of an
// enumeration, or true and false.
func schemaValues(want cue.Value) []Completion {
	var out []Completion
	add := func(v cue.Value) {
		if !v.IsConcrete() {
			return
		}
		b, err := format.Node(v.Syntax())
		if err != nil {
			return
		}
		out = append(out, Completion{Label: string(b), Insert: string(b), Detail: kindName(v), Doc: "A value the schema takes."})
	}
	if op, args := want.Expr(); op == cue.OrOp {
		for _, a := range args {
			add(a)
		}
	} else if want.IncompleteKind() == cue.BoolKind {
		for _, b := range []string{"true", "false"} {
			out = append(out, Completion{Label: b, Insert: b, Detail: "bool"})
		}
	}
	return out
}

// referencesOfKind are the fields under v, as references from prefix, whose
// kind can be want's, to a few levels.
func referencesOfKind(v cue.Value, prefix string, want cue.Kind, depth int) []Completion {
	if want == cue.TopKind || want == cue.BottomKind || depth > 3 {
		return nil
	}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		f := it.Value()
		ref := prefix + "." + it.Selector().String()
		kind := f.IncompleteKind()
		if kind == cue.StructKind && want != cue.StructKind {
			out = append(out, referencesOfKind(f, ref, want, depth+1)...)
			continue
		}
		if kind != cue.TopKind && kind&want != 0 && kind&^want == 0 {
			out = append(out, Completion{Label: ref, Insert: ref, Detail: kindName(f), Doc: usageOf(f)})
		}
	}
	return out
}

// contextOfKind are the context fields of a definition type whose type is
// want, as references.
func contextOfKind(defType string, want cue.Kind) []Completion {
	kinds := map[string]cue.Kind{"string": cue.StringKind, "int": cue.IntKind, "bool": cue.BoolKind}
	var out []Completion
	for _, f := range ContextFields(defType) {
		if k, ok := kinds[f.Type]; ok && !f.Hidden && want != cue.TopKind && k&want != 0 {
			ref := contextLabel + "." + f.Name
			out = append(out, Completion{Label: ref, Insert: ref, Detail: f.Type, Doc: f.Doc})
		}
	}
	return out
}

// filterTyped keeps the completions that begin with what is typed, each
// replacing it, in a stable order: values, then references.
func filterTyped(cs []Completion, typed string) []Completion {
	var out []Completion
	seen := map[string]bool{}
	for _, c := range cs {
		if seen[c.Label] || !strings.HasPrefix(strings.ToLower(c.Label), strings.ToLower(typed)) {
			continue
		}
		seen[c.Label] = true
		c.Replace = len(typed)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// rank orders a completion: values and types first, then parameters, then
// context.
func rank(c Completion) int {
	switch {
	case strings.HasPrefix(c.Label, parameterLabel+"."):
		return 1
	case strings.HasPrefix(c.Label, contextLabel+"."):
		return 2
	}
	return 0
}
