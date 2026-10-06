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

// Package analysis checks a CUE X-Definition file the way the controller would
// compile it, reporting each problem at its position in the file. It knows
// nothing about editors; the language server and the CLI sit on top of it.
package analysis

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// Position is a 1-based line and byte column, as CUE reports them.
type Position struct {
	Line   int
	Column int
}

// Range is the span a diagnostic covers.
type Range struct {
	Start Position
	End   Position
}

// Severity of a diagnostic.
type Severity int

const (
	// SeverityError is a mistake the controller would reject.
	SeverityError Severity = 1
)

// Diagnostic is one problem found in a definition file.
type Diagnostic struct {
	Range    Range
	Severity Severity
	Message  string
}

// Result is what Analyze found.
type Result struct {
	// IsDefinition is false for CUE files that are not X-Definitions; they get
	// no diagnostics, so the server can be attached to every .cue file.
	IsDefinition bool
	Name         string
	Type         string
	Diagnostics  []Diagnostic
}

const (
	templateLabel  = "template"
	parameterLabel = "parameter"
)

var (
	templateLine = regexp.MustCompile(`(?m)^template\s*:`)
	typeLine     = regexp.MustCompile(`(?m)^\s+type\s*:`)
)

// document is a parsed definition file.
type document struct {
	path     string
	src      []byte
	imports  []*ast.ImportDecl
	headers  []*ast.Field
	template *ast.Field
	name     string
	typ      string
}

// Analyze checks the definition in src, read from path.
func Analyze(path string, src []byte) Result {
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		if !templateLine.Match(src) || !typeLine.Match(src) {
			return Result{}
		}
		d := &document{path: path, src: src}
		return Result{IsDefinition: true, Diagnostics: d.fromErrors(err, "")}
	}
	d, ok := newDocument(path, src, f)
	if !ok {
		return Result{}
	}
	res := Result{IsDefinition: true, Name: d.name, Type: d.typ}
	diags := d.checkHeader()
	diags = append(diags, d.checkTemplate()...)
	res.Diagnostics = sortDiagnostics(firstPerPosition(diags))
	return res
}

// newDocument recognises a definition: a top-level template struct beside at
// least one other top-level struct, the header.
func newDocument(path string, src []byte, f *ast.File) (*document, bool) {
	d := &document{path: path, src: src}
	for _, decl := range f.Decls {
		switch x := decl.(type) {
		case *ast.ImportDecl:
			d.imports = append(d.imports, x)
		case *ast.Field:
			if _, ok := x.Value.(*ast.StructLit); !ok {
				continue
			}
			if labelName(x.Label) == templateLabel {
				d.template = x
			} else {
				d.headers = append(d.headers, x)
			}
		}
	}
	if d.template == nil || len(d.headers) == 0 {
		return nil, false
	}
	d.name = labelName(d.headers[0].Label)
	if t, ok := fieldIn(d.headers[0], "type"); ok {
		if lit, ok := t.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			d.typ, _ = literal.Unquote(lit.Value)
		}
	}
	return d, true
}

// compileFile is the template as the controller compiles it: the file's imports
// and its template, with the context for the definition type injected. The
// user's nodes are reused, so every error keeps its position in the file.
func (d *document) compileFile() *ast.File {
	tmpl := d.template.Value.(*ast.StructLit)
	// context goes first: CUE reports a missing field on a closed struct as
	// incomplete, so not at all, when the struct is declared after the read.
	tmpl.Elts = append([]ast.Decl{contextField(d.typ)}, tmpl.Elts...)
	decls := make([]ast.Decl, 0, len(d.imports)+2)
	for _, imp := range d.imports {
		decls = append(decls, imp)
	}
	decls = append(decls, d.template)
	if _, ok := fieldIn(d.template, parameterLabel); ok {
		decls = append(decls, closedParameterField())
	}
	return &ast.File{Filename: d.path, Decls: decls}
}

// maxRecoveries bounds how many times errors are blanked out and the template
// recompiled, so that one bad reference does not hide every error after it.
const maxRecoveries = 20

func (d *document) checkTemplate() []Diagnostic {
	f := d.compileFile()
	var diags []Diagnostic
	for i := 0; ; i++ {
		bi := build.NewContext().NewInstance(d.path, nil)
		bi.Imports = packagesFor(d.typ).imports()
		if err := bi.AddSyntax(f); err != nil {
			return append(diags, d.fromErrors(err, "")...)
		}
		v := cuecontext.New().BuildInstance(bi)
		if err := v.Err(); err != nil && i < maxRecoveries && blankOut(f, cueerrors.Errors(err)) {
			diags = append(diags, d.fromErrors(err, templateLabel)...)
			continue
		}
		diags = append(diags, d.fromErrors(v.Validate(), templateLabel)...)
		return append(diags, d.checkSelectors(v)...)
	}
}

// fromErrors turns CUE errors into diagnostics at each position they name in
// this file. A conflict names both sides, so it is reported at both.
func (d *document) fromErrors(err error, trimPath string) []Diagnostic {
	var diags []Diagnostic
	seen := map[string]bool{}
	for _, e := range cueerrors.Errors(err) {
		format, args := e.Msg()
		msg := fmt.Sprintf(format, args...)
		if p := strings.Join(trimLabel(e.Path(), trimPath), "."); p != "" && !strings.HasPrefix(p, "#") {
			msg = p + ": " + msg
		}
		positions := append([]token.Pos{e.Position()}, e.InputPositions()...)
		for _, pos := range positions {
			if !pos.IsValid() || pos.Filename() != d.path {
				continue
			}
			key := fmt.Sprintf("%d:%d:"+format, append([]interface{}{pos.Line(), pos.Column()}, args...)...)
			if seen[key] {
				continue
			}
			seen[key] = true
			diags = append(diags, d.at(pos, msg))
		}
	}
	return diags
}

// at builds a diagnostic spanning the token that starts at pos.
func (d *document) at(pos token.Pos, msg string) Diagnostic {
	start := Position{Line: pos.Line(), Column: pos.Column()}
	return Diagnostic{Range: Range{Start: start, End: tokenEnd(d.src, start)}, Severity: SeverityError, Message: msg}
}

// tokenEnd is the end of the word or quoted string starting at p.
func tokenEnd(src []byte, p Position) Position {
	line := 1
	off := 0
	for off < len(src) && line < p.Line {
		if src[off] == '\n' {
			line++
		}
		off++
	}
	off += p.Column - 1
	end := off
	if end < len(src) && src[end] == '"' {
		end++
		for end < len(src) && src[end] != '"' && src[end] != '\n' {
			end++
		}
		if end < len(src) && src[end] == '"' {
			end++
		}
	} else {
		for end < len(src) && isWordByte(src[end]) {
			end++
		}
	}
	if end == off {
		end++
	}
	return Position{Line: p.Line, Column: p.Column + end - off}
}

func isWordByte(b byte) bool {
	return b == '_' || b == '#' || b == '$' || b == '.' || b == '/' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func trimLabel(path []string, label string) []string {
	if label != "" && len(path) > 0 && path[0] == label {
		return path[1:]
	}
	return path
}

// firstPerPosition keeps one diagnostic per position: CUE often reports one
// mistake twice, as with an unknown import being both undefined and not found.
func firstPerPosition(diags []Diagnostic) []Diagnostic {
	seen := map[Position]bool{}
	out := diags[:0]
	for _, d := range diags {
		if !seen[d.Range.Start] {
			seen[d.Range.Start] = true
			out = append(out, d)
		}
	}
	return out
}

func sortDiagnostics(diags []Diagnostic) []Diagnostic {
	sort.SliceStable(diags, func(i, j int) bool {
		a, b := diags[i].Range.Start, diags[j].Range.Start
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return diags
}

// labelName is a field label as written, with any quotes removed.
func labelName(l ast.Label) string {
	switch x := l.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.BasicLit:
		if s, err := literal.Unquote(x.Value); err == nil {
			return s
		}
		return x.Value
	}
	return ""
}

// fieldIn finds a direct child field of a struct-valued field.
func fieldIn(f *ast.Field, name string) (*ast.Field, bool) {
	s, ok := f.Value.(*ast.StructLit)
	if !ok {
		return nil, false
	}
	for _, elt := range s.Elts {
		if c, ok := elt.(*ast.Field); ok && labelName(c.Label) == name {
			return c, true
		}
	}
	return nil, false
}

// lookup is cue.Value.LookupPath for one selector.
func lookup(v cue.Value, s cue.Selector) cue.Value {
	return v.LookupPath(cue.MakePath(s))
}
