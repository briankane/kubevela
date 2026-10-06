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
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// DefinitionLookup finds a definition by name: where it is, and its text.
type DefinitionLookup func(name string) (path string, src []byte, ok bool)

// superLabel is where a definition that extends another speaks to its parent.
const superLabel = "$super"

// parentName is the definition this one extends, without a pinned revision.
func (d *document) parentName() string {
	name := d.headerString("extends")
	if name == "" {
		name = d.headerString("attributes", "extends")
	}
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	return name
}

// extendsPos is where the header names its parent.
func (d *document) extendsPos() token.Pos {
	for _, path := range [][]string{{"extends"}, {"attributes", "extends"}} {
		f := d.headers[0]
		found := true
		for _, p := range path {
			child, ok := fieldIn(f, p)
			if !ok {
				found = false
				break
			}
			f = child
		}
		if found {
			return f.Label.Pos()
		}
	}
	return d.headers[0].Label.Pos()
}

// checkExtends checks a component that extends another: that it declares
// $super, and that $super.properties is what the parent's parameter takes.
func (d *document) checkExtends() []Diagnostic {
	parent := d.parentName()
	if d.typ != componentType || parent == "" {
		return nil
	}
	declared := map[string][]*ast.Field{}
	allTopLevelFields(d.template.Value, declared)
	supers := declared[superLabel]
	if len(supers) == 0 {
		return []Diagnostic{d.at(d.template.Label.Pos(),
			fmt.Sprintf("a component that extends %s must declare $super, with the properties %s takes: $super: properties: {...}", parent, parent))}
	}
	if d.opts.Definitions == nil {
		return nil
	}
	ctx := cuecontext.New()
	params, ok := d.parentParameter(ctx, parent)
	if !ok {
		diag := d.at(d.extendsPos(), fmt.Sprintf("%s is not in the workspace, so the properties passed to it are not checked", parent))
		diag.Severity = SeverityInfo
		return []Diagnostic{diag}
	}
	var props *ast.Field
	for _, s := range supers {
		if p, ok := fieldIn(s, "properties"); ok {
			props = p
		}
	}
	at := supers[0].Label.Pos()
	if props != nil {
		at = props.Label.Pos()
	}

	var diags []Diagnostic
	if props != nil {
		diags = append(diags, d.unknownParameters(props, params, parent, nil)...)
	}
	v, ok := d.evaluateIn(ctx)
	if !ok {
		return diags
	}
	given := v.LookupPath(cue.MakePath(cue.Str(templateLabel), cue.Str(superLabel), cue.Str("properties")))
	if given.Exists() {
		for _, e := range cueerrors.Errors(given.Unify(params).Validate()) {
			diags = append(diags, d.fromErrors(e, templateLabel)...)
		}
	}
	it, err := params.Fields()
	if err != nil {
		return diags
	}
	var missing []string
	for it.Next() {
		if _, hasDefault := it.Value().Default(); hasDefault {
			continue
		}
		if !given.LookupPath(cue.MakePath(it.Selector())).Exists() {
			missing = append(missing, it.Selector().Unquoted())
		}
	}
	if len(missing) > 0 {
		diags = append(diags, d.at(at, fmt.Sprintf("%s requires %s in $super.properties", parent, strings.Join(missing, ", "))))
	}
	return diags
}

// unknownParameters reports each property under f the parent's parameter
// does not declare, at any depth written as a struct.
func (d *document) unknownParameters(f *ast.Field, params cue.Value, parent string, path []string) []Diagnostic {
	s, ok := f.Value.(*ast.StructLit)
	if !ok || params.IncompleteKind() != cue.StructKind {
		return nil
	}
	var diags []Diagnostic
	for _, elt := range s.Elts {
		child, ok := elt.(*ast.Field)
		if !ok {
			continue
		}
		name := labelName(child.Label)
		at := strings.Join(append(append([]string{}, path...), name), ".")
		if !params.Allows(cue.Str(name)) {
			diags = append(diags, d.at(child.Label.Pos(), fmt.Sprintf("%s takes no parameter %s", parent, at)))
			continue
		}
		diags = append(diags, d.unknownParameters(child, schemaChild(params, cue.Str(name)), parent, append(path, name))...)
	}
	return diags
}

// parentParameter is the closed parameter of the parent definition, built in
// ctx so a child's values can be unified with it.
func (d *document) parentParameter(ctx *cue.Context, name string) (cue.Value, bool) {
	path, src, ok := d.opts.Definitions(name)
	if !ok {
		return cue.Value{}, false
	}
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return cue.Value{}, false
	}
	pd, ok := newDocument(path, src, f)
	if !ok || pd.typ != d.typ {
		return cue.Value{}, false
	}
	pd.opts = Options{Externals: d.opts.Externals}
	v, ok := pd.evaluateIn(ctx)
	if !ok {
		return cue.Value{}, false
	}
	params := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	return params, params.Exists()
}

// evaluateIn compiles the template as checkTemplate does, in ctx.
func (d *document) evaluateIn(ctx *cue.Context) (cue.Value, bool) {
	bi := build.NewContext().NewInstance(d.path, nil)
	bi.Imports = d.packages().imports()
	if err := bi.AddSyntax(d.compileFile()); err != nil {
		return cue.Value{}, false
	}
	return ctx.BuildInstance(bi), true
}

var superPropertiesTyped = regexp.MustCompile(`\$super:\s*properties:\s*(?:\{\s*)?([A-Za-z0-9_]*)$`)

// CompleteSuperProperties completes a property passed to the parent, given
// the document and its line up to the cursor: the parameters the parent
// takes, with their docs.
func CompleteSuperProperties(doc, before string, opts Options) []Completion {
	m := superPropertiesTyped.FindStringSubmatch(before)
	if m == nil || opts.Definitions == nil {
		return nil
	}
	f, err := parser.ParseFile("complete.cue", doc, parser.ParseComments)
	if err != nil {
		return nil
	}
	d, ok := newDocument("complete.cue", []byte(doc), f)
	if !ok {
		return nil
	}
	d.opts = opts
	params, ok := d.parentParameter(cuecontext.New(), d.parentName())
	if !ok {
		return nil
	}
	it, err := params.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		name := it.Selector().Unquoted()
		if strings.HasPrefix(name, m[1]) {
			out = append(out, Completion{Label: name, Insert: name, Replace: len(m[1]), Detail: kindName(it.Value()), Doc: usageOf(it.Value())})
		}
	}
	return out
}

// DefinitionHeader is the name and type of the definition in src, for
// indexing a workspace.
func DefinitionHeader(path string, src []byte) (name, defType string, ok bool) {
	f, err := parser.ParseFile(path, src)
	if err != nil {
		return "", "", false
	}
	d, ok := newDocument(path, src, f)
	if !ok || d.typ == "" {
		return "", "", false
	}
	return d.name, d.typ, true
}

var extendsTyped = regexp.MustCompile(`\bextends:\s*"([A-Za-z0-9-]*)$`)

// CompleteExtends completes the name a definition extends, from the
// definitions of its type given.
func CompleteExtends(before string, names []string) []Completion {
	m := extendsTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	var out []Completion
	for _, n := range names {
		if strings.HasPrefix(n, m[1]) {
			out = append(out, Completion{Label: n, Insert: n, Replace: len(m[1]), Doc: "Extends the " + n + " definition: $super.properties is what it receives."})
		}
	}
	return out
}
