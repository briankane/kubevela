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
	"embed"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/oam-dev/kubevela/pkg/definition"
	velaast "github.com/oam-dev/kubevela/pkg/definition/ast"
)

// statusPaths are the header fields whose native CUE velaast.EncodeMetadata
// checks; its errors start with the path they are about.
var statusPaths = []string{
	"attributes.status.healthPolicy",
	"attributes.status.customStatus",
	"attributes.status.details",
}

// checkHeader checks the definition header the way `vela def apply` does.
func (d *document) checkHeader() []Diagnostic {
	h := d.headers[0]
	var diags []Diagnostic
	for _, extra := range d.headers[1:] {
		diags = append(diags, d.at(extra.Label.Pos(),
			fmt.Sprintf("a definition file declares one definition; %q is a second", labelName(extra.Label))))
	}
	t, ok := fieldIn(h, "type")
	switch {
	case !ok:
		diags = append(diags, d.at(h.Label.Pos(),
			fmt.Sprintf("missing type: one of %s", strings.Join(definition.ValidDefinitionTypes(), ", "))))
	case definition.DefinitionTypeToKind[d.typ] == "":
		diags = append(diags, d.at(t.Value.Pos(),
			fmt.Sprintf("unknown type %q: one of %s", d.typ, strings.Join(definition.ValidDefinitionTypes(), ", "))))
	}
	if len(diags) > 0 {
		return diags
	}
	return d.checkMetadata()
}

// checkMetadata runs the status checks and builds the Definition from the
// header. Both rewrite the header, so they work on a fresh parse of it.
func (d *document) checkMetadata() []Diagnostic {
	f, err := parser.ParseFile(d.path, d.src, parser.ParseComments)
	if err != nil {
		return nil
	}
	fresh, ok := newDocument(d.path, d.src, f)
	if !ok {
		return nil
	}
	h := fresh.headers[0]
	if err := velaast.EncodeMetadata(h); err != nil {
		return []Diagnostic{d.at(statusPos(h, err.Error()), err.Error())}
	}
	v := cuecontext.New().BuildFile(&ast.File{Filename: d.path, Decls: []ast.Decl{h}})
	if err := v.Err(); err != nil {
		return d.fromErrors(err, "")
	}
	if diags := d.checkHeaderSchema(h); len(diags) > 0 {
		return diags
	}
	// The schema covers the header's shape; FromCUE also checks what it means.
	def := definition.Definition{}
	if err := def.FromCUE(&v, templateLabel); err != nil {
		return []Diagnostic{d.at(h.Label.Pos(), err.Error())}
	}
	return nil
}

//go:embed header.cue header_attributes.cue
var headerSchemaFS embed.FS

// headerSchema is the declarations of #header and #attributes.
var headerSchema = sync.OnceValue(func() []ast.Decl {
	var decls []ast.Decl
	for _, name := range []string{"header.cue", "header_attributes.cue"} {
		src, err := headerSchemaFS.ReadFile(name)
		if err == nil {
			var f *ast.File
			if f, err = parser.ParseFile(name, src); err == nil {
				decls = append(decls, f.Decls...)
				continue
			}
		}
		panic(fmt.Sprintf("embedded %s: %v", name, err))
	}
	return decls
})

// checkHeaderSchema checks the header against #header, with attributes
// closed to what the Definition CRD of its type allows, so a misspelt key or a
// value of the wrong type is reported where it is written.
func (d *document) checkHeaderSchema(h *ast.Field) []Diagnostic {
	constraint := mustField(fmt.Sprintf("%s: #header & {attributes?: #attributes[%q]}", strconv.Quote(d.name), d.typ))
	decls := append([]ast.Decl{h, constraint}, headerSchema()...)
	v := cuecontext.New().BuildFile(&ast.File{Filename: d.path, Decls: decls})
	// Err stops at the first error; Validate walks the whole header, so a
	// misspelt key is reported beside a value of the wrong type.
	errs := append(cueerrors.Errors(v.Err()), cueerrors.Errors(v.LookupPath(cue.MakePath(cue.Str(d.name))).Validate(cue.Concrete(true)))...)
	// CUE stops at a header's first error, so keys are checked on their own:
	// a misspelt key is reported beside a value of the wrong type.
	schema := cuecontext.New().BuildFile(&ast.File{Decls: append([]ast.Decl{mustField(fmt.Sprintf("#this: #header & {attributes?: #attributes[%q]}", d.typ))}, headerSchema()...)})
	diags := d.unknownKeys(h, schema.LookupPath(cue.ParsePath("#this")), nil)
	for _, e := range errs {
		found := d.fromErrors(e, d.name)
		if len(found) == 0 {
			// A missing required field is positioned in the schema: report it
			// at the deepest field of its path that the header does write.
			format, args := e.Msg()
			msg := strings.Join(trimLabel(e.Path(), d.name), ".") + ": " + fmt.Sprintf(format, args...)
			found = []Diagnostic{d.at(writtenAncestor(h, e.Path()), msg)}
		}
		diags = append(diags, found...)
	}
	return diags
}

// unknownKeys reports each key under f that schema does not allow, at every
// depth the header writes as a struct.
func (d *document) unknownKeys(f *ast.Field, schema cue.Value, path []string) []Diagnostic {
	s, ok := f.Value.(*ast.StructLit)
	if !ok || schema.IncompleteKind() != cue.StructKind {
		return nil
	}
	var diags []Diagnostic
	for _, elt := range s.Elts {
		child, ok := elt.(*ast.Field)
		if !ok {
			continue
		}
		name := labelName(child.Label)
		sel := cue.Str(name)
		at := strings.Join(append(append([]string{}, path...), name), ".")
		if !schema.Allows(sel) {
			diags = append(diags, d.at(child.Label.Pos(), at+": field not allowed"))
			continue
		}
		diags = append(diags, d.unknownKeys(child, schemaChild(schema, sel), append(path, name))...)
	}
	return diags
}

// schemaChild is the schema of a field: declared, optional, or by pattern.
func schemaChild(schema cue.Value, sel cue.Selector) cue.Value {
	for _, path := range []cue.Path{cue.MakePath(sel), cue.MakePath(sel.Optional()), cue.MakePath(cue.AnyString)} {
		if v := schema.LookupPath(path); v.Exists() {
			return v
		}
	}
	return cue.Value{}
}

// writtenAncestor is the position of the deepest field on path that the
// header writes, or of the header's name; path starts with that name.
func writtenAncestor(h *ast.Field, path []string) token.Pos {
	pos := h.Label.Pos()
	f := h
	for _, label := range path[min(1, len(path)):] {
		if u, err := strconv.Unquote(label); err == nil {
			label = u
		}
		child, ok := fieldIn(f, label)
		if !ok {
			break
		}
		f, pos = child, child.Label.Pos()
	}
	return pos
}

// statusPos is the status field an EncodeMetadata error is about.
func statusPos(h *ast.Field, msg string) token.Pos {
	for _, p := range statusPaths {
		if !strings.HasPrefix(msg, p) {
			continue
		}
		if f, ok := velaast.GetFieldByPath(h, p); ok {
			return f.Pos()
		}
	}
	return h.Label.Pos()
}
