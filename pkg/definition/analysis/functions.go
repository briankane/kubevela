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
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
)

// paramsLabel holds what a call of a provider function sends it.
const paramsLabel = "$params"

// functionInputs is what a call of a package function gives it, and the
// field that holds it: a provider function's $params, or, for a function of
// plain CUE, the function itself, whose fields that are not derived are its
// inputs.
func functionInputs(fn cue.Value) (cue.Value, string) {
	if params := fn.LookupPath(cue.MakePath(cue.Str(paramsLabel))); params.Exists() {
		return params, paramsLabel
	}
	return fn, ""
}

// legacyProvider reports whether fn is a provider function with no $params,
// as the legacy vela/op ones are: what it takes and what the provider fills
// in are fields alike, so none can be told required.
func legacyProvider(fn cue.Value) bool {
	_, wrapper := functionInputs(fn)
	return wrapper == "" && fn.LookupPath(cue.MakePath(cue.Def("#do"))).Exists()
}

// derived reports whether v is no input of the function declaring it: it
// is computed from other values, as out in out: name + "-" + suffix, or a
// struct built from them, or it is a step the function runs, a provider
// call. A reference to a definition, an import or a builtin is a type, not
// a value, and does not count.
func derived(v cue.Value) bool {
	if v.LookupPath(cue.MakePath(cue.Def("#do"))).Exists() {
		return true
	}
	switch src := v.Source().(type) {
	case *ast.Field:
		return refersToField(src.Value)
	case ast.Expr:
		return refersToField(src)
	}
	if _, path := v.ReferencePath(); len(path.Selectors()) > 0 {
		return true
	}
	op, args := v.Expr()
	if op == cue.NoOp {
		return false
	}
	for _, a := range args {
		if derived(a) {
			return true
		}
	}
	return false
}

// refersToField reports whether e refers to a field that is no definition.
func refersToField(e ast.Expr) bool {
	found := false
	ast.Walk(e, func(n ast.Node) bool {
		if found {
			return false
		}
		id, ok := n.(*ast.Ident)
		if !ok || id.Node == nil || strings.HasPrefix(id.Name, "#") {
			return true
		}
		if _, isImport := id.Node.(*ast.ImportSpec); !isImport {
			found = true
		}
		return true
	}, nil)
	return found
}

// shape is a struct's fields and their kinds, briefly: {name: string}.
// derivedOnly selects the derived fields, else the inputs.
func shape(v cue.Value, derivedOnly bool) []string {
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []string
	for it.Next() {
		if derived(it.Value()) != derivedOnly {
			continue
		}
		name := it.Selector().String()
		if it.IsOptional() {
			name += "?"
		}
		// A derived field's kind is unknown until its inputs are given, as
		// + adds numbers and joins strings alike: then it is named alone.
		if kind := kindName(it.Value()); kind != "_" || !derivedOnly {
			name += ": " + kind
		}
		out = append(out, name)
	}
	return out
}

// describeMember summarises a package function: what it takes and gives as
// its detail, and as its doc, its +usage and its CUE as written.
func describeMember(v cue.Value) (detail, doc string) {
	inputs, wrapper := functionInputs(v)
	if wrapper != "" {
		detail = wrapper + ": {" + strings.Join(shape(inputs, false), ", ") + "}"
		if returns := v.LookupPath(cue.MakePath(cue.Str("$returns"))); returns.Exists() {
			detail += " → $returns: {" + strings.Join(shape(returns, false), ", ") + "}"
		}
	} else {
		detail = "{" + strings.Join(shape(v, false), ", ") + "}"
		if out := shape(v, true); len(out) > 0 {
			detail += " → " + strings.Join(out, ", ")
		}
	}
	doc = usageOf(v)
	if src := v.Source(); src != nil {
		if b, err := format.Node(src); err == nil {
			if doc != "" {
				doc += "\n\n"
			}
			doc += "```cue\n" + string(b) + "\n```"
		}
	}
	return detail, doc
}

// CompleteInCall completes a field of a call of a package function at
// cursor in doc, where a label goes: in pkg.#Fn & {|}, the function's inputs
// (for a provider function, $params), and in a struct inside it, that
// struct's fields; those already given are left out.
func CompleteInCall(doc string, cursor int, ext *Externals) []Completion {
	start := cursor
	for start > 0 && (isWordByteAt(doc[start-1]) || doc[start-1] == '$') {
		start--
	}
	typed := doc[start:cursor]
	if prev := strings.TrimRight(doc[:start], " \t"); prev == "" || !strings.ContainsAny(prev[len(prev)-1:], "{,\n") {
		return nil
	}
	f, err := parser.ParseFile("call.cue", doc[:start]+cursorPlaceholder+": _"+doc[cursor:])
	if err != nil {
		return nil
	}
	call, path, siblings, ok := callAround(f)
	if !ok {
		return nil
	}
	root, chain := flatten(call)
	if root == nil || len(chain) != 1 {
		return nil
	}
	importPath := ""
	for _, spec := range importSpec.FindAllStringSubmatch(doc, -1) {
		alias := spec[1]
		if alias == "" {
			alias = spec[2][strings.LastIndex(spec[2], "/")+1:]
		}
		if alias == root.Name {
			importPath = spec[2]
		}
	}
	kind, ok := completionKind(doc)
	if importPath == "" || !ok {
		return nil
	}
	pkgs := packages{builtin: packagesFor(kind), ext: ext}
	pkg, ok := pkgs.value(importPath)
	if !ok {
		return nil
	}
	fn := pkg.LookupPath(cue.MakePath(cue.Def(chain[0].Name)))
	documented := fn
	if dp, ok := pkgs.documented(importPath); ok {
		if v := dp.LookupPath(cue.MakePath(cue.Def(chain[0].Name))); v.Exists() {
			documented = v
		}
	}
	if !fn.Exists() {
		return nil
	}
	target, docTarget := fn, documented
	if len(path) == 0 {
		if _, wrapper := functionInputs(fn); wrapper != "" {
			return fieldCompletion(paramsLabel, fn.LookupPath(cue.MakePath(cue.Str(paramsLabel))), documented.LookupPath(cue.MakePath(cue.Str(paramsLabel))), typed, siblings)
		}
	}
	for _, p := range path {
		sel := cue.Str(p)
		target, docTarget = target.LookupPath(cue.MakePath(sel)), docTarget.LookupPath(cue.MakePath(sel))
	}
	it, err := target.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		name := it.Selector().Unquoted()
		if derived(it.Value()) {
			continue
		}
		out = append(out, fieldCompletion(name, it.Value(), docTarget.LookupPath(cue.MakePath(it.Selector())), typed, siblings)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// fieldCompletion is a field to complete, unless already given or not what
// was typed.
func fieldCompletion(name string, v, documented cue.Value, typed string, given map[string]bool) []Completion {
	if given[name] || !strings.HasPrefix(name, typed) {
		return nil
	}
	snippet := snippetEscape(name) + ": $0"
	if v.IncompleteKind() == cue.StructKind {
		snippet = snippetEscape(name) + ": {\n\t$0\n}"
	}
	return []Completion{{Label: name, Insert: name, Replace: len(typed), Detail: kindName(v), Doc: usageOf(documented), Snippet: snippet}}
}

// callAround finds the placeholder field in f and the call it is written in:
// the function called, the labels from the call's struct down to the
// placeholder's, and the labels the placeholder's struct already has.
func callAround(f *ast.File) (*ast.SelectorExpr, []string, map[string]bool, bool) {
	var stack, found []ast.Node
	ast.Walk(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		stack = append(stack, n)
		if fd, ok := n.(*ast.Field); ok && labelName(fd.Label) == cursorPlaceholder {
			found = append([]ast.Node{}, stack...)
			return false
		}
		return true
	}, func(ast.Node) { stack = stack[:len(stack)-1] })
	if len(found) < 2 {
		return nil, nil, nil, false
	}
	siblings := map[string]bool{}
	if s, ok := found[len(found)-2].(*ast.StructLit); ok {
		for _, e := range s.Elts {
			if fd, ok := e.(*ast.Field); ok {
				siblings[labelName(fd.Label)] = true
			}
		}
	}
	var path []string
	for i := len(found) - 2; i >= 0; i-- {
		switch x := found[i].(type) {
		case *ast.Field:
			path = append([]string{labelName(x.Label)}, path...)
		case *ast.BinaryExpr:
			if sel, ok := x.X.(*ast.SelectorExpr); ok && x.Op.String() == "&" && i+1 < len(found) && found[i+1] == x.Y {
				return sel, path, siblings, true
			}
			return nil, nil, nil, false
		}
	}
	return nil, nil, nil, false
}
