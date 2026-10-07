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
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/literal"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// statusField is one of a definition's status fields, healthPolicy,
// customStatus or details: the CUE it holds, and where that is written in
// the file, as a string or as native CUE.
type statusField struct {
	name  string
	text  string
	label *ast.Field
	// line is the file's line of the text's first line. firstCol is what
	// the first line's columns are offset by in the file, col the others'.
	line, firstCol, col int
	// start and end are the offsets in the file the text lies within.
	start, end int
}

// at is the position in the file of a position in the field's text.
func (f statusField) at(pos token.Pos) Position {
	return f.atPosition(Position{Line: pos.Line(), Column: pos.Column()})
}

// atPosition is the position in the file of a line and column of the
// field's text.
func (f statusField) atPosition(p Position) Position {
	if p.Line == 1 {
		return Position{Line: f.line, Column: p.Column + f.firstCol}
	}
	return Position{Line: f.line + p.Line - 1, Column: p.Column + f.col}
}

// offsetIn is the offset in the field's text of an offset in the file, or
// false when the file's offset is not in the text.
func (f statusField) offsetIn(src []byte, offset int) (int, bool) {
	if offset < f.start || offset > f.end {
		return 0, false
	}
	line := strings.Count(string(src[:offset]), "\n") + 1
	col := offset - strings.LastIndex(string(src[:offset]), "\n")
	l := line - f.line + 1
	c := col - f.col
	if l == 1 {
		c = col - f.firstCol
	}
	lines := strings.SplitAfter(f.text, "\n")
	if l < 1 || l > len(lines) || c < 1 {
		return 0, false
	}
	out := 0
	for _, prev := range lines[:l-1] {
		out += len(prev)
	}
	return min(out+c-1, len(f.text)), true
}

// statusFieldNames are the status fields KubeVela evaluates, and the field
// each must set.
var statusFieldNames = []string{"healthPolicy", "customStatus", "details"}

// statusFields are the status fields the definition writes.
func (d *document) statusFields() []statusField {
	if len(d.headers) == 0 {
		return nil
	}
	attrs, ok := fieldIn(d.headers[0], "attributes")
	if !ok {
		return nil
	}
	st, ok := fieldIn(attrs, "status")
	if !ok {
		return nil
	}
	var out []statusField
	for _, name := range statusFieldNames {
		f, ok := fieldIn(st, name)
		if !ok {
			continue
		}
		sf := statusField{name: name, label: f}
		switch v := f.Value.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				continue
			}
			text, err := literal.Unquote(v.Value)
			if err != nil {
				continue
			}
			sf.text, sf.start, sf.end = text, v.Pos().Offset(), v.End().Offset()
			raw := strings.TrimLeft(v.Value, "#")
			if strings.HasPrefix(raw, `"""`) {
				lines := strings.Split(v.Value, "\n")
				closing := lines[len(lines)-1]
				indent := len(closing) - len(strings.TrimLeft(closing, " \t"))
				sf.line, sf.firstCol, sf.col = v.Pos().Line()+1, indent, indent
			} else {
				sf.line = v.Pos().Line()
				sf.firstCol = v.Pos().Column() - 1 + len(v.Value) - len(raw) + 1
			}
		case *ast.StructLit:
			if !v.Lbrace.IsValid() || !v.Rbrace.IsValid() {
				continue
			}
			sf.text = string(d.src[v.Lbrace.Offset()+1 : v.Rbrace.Offset()])
			sf.start, sf.end = v.Lbrace.Offset()+1, v.Rbrace.Offset()
			sf.line, sf.firstCol, sf.col = v.Lbrace.Line(), v.Lbrace.Column(), 0
		default:
			continue
		}
		out = append(out, sf)
	}
	return out
}

// statusContextCUE is the context KubeVela evaluates a status field with:
// the render context's fields, the live output, typed by the kind the
// template outputs, the live outputs by name, and status. The controller
// gives each, so none is optional: a read through an optional field fails.
func (d *document) statusContextCUE() string {
	var b strings.Builder
	b.WriteString("context: #velaStatusContext\nparameter: _\n#velaStatusContext: {\n")
	output := "{...}"
	extra := ""
	if gvk, ok := d.outputKind(); ok {
		kinds := d.opts.Kinds
		if kinds == nil {
			kinds = bundledKinds()
		}
		if src, ok := kinds.CUE(gvk); ok {
			output, extra = kubeschema.Root(gvk), src
		}
	}
	for _, f := range ContextFields(d.typ) {
		switch f.Name {
		case "output":
			fmt.Fprintf(&b, "\toutput: %s\n", output)
		case "outputs":
			b.WriteString("\toutputs: [string]: {...}\n")
		default:
			fmt.Fprintf(&b, "\t%s: %s\n", strconv.Quote(f.Name), f.Type)
		}
	}
	b.WriteString("\tstatus: {healthy: bool, ...}\n")
	// The controller sets context.parameter to the parameters too.
	b.WriteString("\tparameter: _\n}\n")
	return b.String() + extra
}

// outputKind is the kind the template's output declares, when written as
// literals.
func (d *document) outputKind() (kubeschema.GVK, bool) {
	if d.template == nil {
		return kubeschema.GVK{}, false
	}
	out, ok := fieldIn(d.template, "output")
	if !ok {
		return kubeschema.GVK{}, false
	}
	s, ok := out.Value.(*ast.StructLit)
	if !ok {
		return kubeschema.GVK{}, false
	}
	apiVersion, kind := literalField(s, "apiVersion"), literalField(s, "kind")
	if apiVersion == "" || kind == "" {
		return kubeschema.GVK{}, false
	}
	return kubeschema.ParseGVK(apiVersion, kind), true
}

// statusName is the name a status field's CUE is parsed under.
func (d *document) statusName(f statusField) string {
	return d.path + "#" + f.name
}

// statusValue is a status field's text compiled as KubeVela compiles it,
// with the template's closed parameter, and that parameter.
func (d *document) statusValue(f statusField, text string) (*ast.File, cue.Value, cue.Value, error) {
	file, err := parser.ParseFile(d.statusName(f), text, parser.ParseComments)
	if err != nil {
		return nil, cue.Value{}, cue.Value{}, err
	}
	if d.statusCtx == nil {
		d.statusSchema = d.statusContextCUE()
	}
	schema, err := parser.ParseFile("vela-status-context.cue", d.statusSchema)
	if err != nil {
		return nil, cue.Value{}, cue.Value{}, err
	}
	// One evaluation of the template serves each status field.
	if d.statusCtx == nil {
		d.statusCtx = cuecontext.New()
		if tv, ok := d.evaluateIn(d.statusCtx); ok {
			d.statusParam = tv.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
		}
		d.statusSchema = d.statusContextCUE()
	}
	ctx, param := d.statusCtx, d.statusParam
	combined := &ast.File{Filename: file.Filename, Decls: append(append([]ast.Decl{}, file.Decls...), schema.Decls...)}
	bi := build.NewContext().NewInstance(d.path, nil)
	if err := bi.AddSyntax(combined); err != nil {
		return file, cue.Value{}, param, err
	}
	v := ctx.BuildInstance(bi)
	if param.Exists() {
		v = v.FillPath(cue.ParsePath(parameterLabel), param)
		v = v.FillPath(cue.ParsePath("context."+parameterLabel), param)
	}
	return file, v, param, nil
}

// statusDiag is a diagnostic at a position in a status field's text.
func (d *document) statusDiag(f statusField, pos token.Pos, msg string) Diagnostic {
	start := f.at(pos)
	return Diagnostic{Range: Range{Start: start, End: tokenEnd(d.src, start)}, Severity: SeverityError, Message: msg}
}

// runtimeOnly matches the errors of a read of what only the live objects
// hold: the schema says what they may be, not what they are, so such a read
// is incomplete here, not wrong. A misspelt read is the read check's to find.
var runtimeOnly = regexp.MustCompile(`undefined field|cannot reference optional field|incomplete|non-concrete|not concrete`)

// statusErrors are the diagnostics of CUE errors in a status field's text.
func (d *document) statusErrors(f statusField, err error) []Diagnostic {
	var diags []Diagnostic
	for _, e := range cueerrors.Errors(err) {
		format, args := e.Msg()
		msg := fmt.Sprintf(format, args...)
		if runtimeOnly.MatchString(msg) {
			continue
		}
		if p := strings.Join(e.Path(), "."); p != "" && !strings.HasPrefix(p, "#") {
			msg = p + ": " + msg
		}
		for _, pos := range append([]token.Pos{e.Position()}, e.InputPositions()...) {
			if pos.IsValid() && pos.Filename() == d.statusName(f) {
				diags = append(diags, d.statusDiag(f, pos, f.name+": "+msg))
				break
			}
		}
	}
	return diags
}

// checkStatusFields checks each status field as KubeVela evaluates it: its
// CUE, against the standard library only; its reads of context and
// parameter, the live output typed by its kind; and the field it must set.
func (d *document) checkStatusFields() []Diagnostic {
	if d.typ != componentType && d.typ != traitType {
		return nil
	}
	var diags []Diagnostic
	for _, f := range d.statusFields() {
		file, v, param, err := d.statusValue(f, f.text)
		if file == nil {
			diags = append(diags, d.statusErrors(f, err)...)
			continue
		}
		vela := false
		for _, spec := range file.Imports {
			if p := strings.Trim(spec.Path.Value, `"`); strings.HasPrefix(p, "vela/") {
				diags = append(diags, d.statusDiag(f, spec.Path.Pos(), fmt.Sprintf("%s compiles with CUE's standard library only: %s is not available", f.name, p)))
				vela = true
			}
		}
		if vela || err != nil {
			diags = append(diags, d.statusErrors(f, err)...)
			continue
		}
		diags = append(diags, d.statusErrors(f, v.Err())...)
		diags = append(diags, d.statusErrors(f, v.Validate())...)
		diags = append(diags, d.statusReads(f, file, v, param)...)
		diags = append(diags, d.statusResult(f, file, v)...)
		if f.name == "details" {
			diags = append(diags, d.checkDetails(f, file)...)
		}
	}
	return diags
}

// statusReads checks each context and parameter chain a status field reads
// against their closed declarations.
func (d *document) statusReads(f statusField, file *ast.File, v, param cue.Value) []Diagnostic {
	roots := map[string]cue.Value{contextLabel: v.LookupPath(cue.ParsePath(contextLabel))}
	if param.Exists() {
		roots[parameterLabel] = param
	}
	var diags []Diagnostic
	ast.Walk(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root, chain := flatten(sel)
		if root == nil {
			return true
		}
		cur, ok := roots[root.Name]
		if !ok {
			return false
		}
		walked := []string{root.Name}
		for _, id := range chain {
			if cur.IncompleteKind() != cue.StructKind {
				break
			}
			s, ok := selectorFor(id.Name)
			if !ok {
				break
			}
			if !cur.Allows(s) {
				diag := d.statusDiag(f, id.Pos(), fmt.Sprintf("%s: %s has no field %s", f.name, strings.Join(walked, "."), id.Name))
				names := fieldNames(cur)
				if len(walked) == 1 && root.Name == contextLabel {
					names = offeredContext(names, d.typ)
				}
				if to := closest(id.Name, names); to != "" {
					start := f.at(id.Pos())
					diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: Range{Start: start, End: Position{Line: start.Line, Column: start.Column + len(id.Name)}}, NewText: to}}}}
				}
				diags = append(diags, diag)
				break
			}
			cur = schemaChild(cur, s)
			walked = append(walked, id.Name)
		}
		return false
	}, nil)
	return diags
}

// statusResult checks the field a status field must set: healthPolicy's
// isHealth, a bool, and customStatus's message, a string.
func (d *document) statusResult(f statusField, file *ast.File, v cue.Value) []Diagnostic {
	var want, what string
	var kind cue.Kind
	switch f.name {
	case "healthPolicy":
		want, kind, what = "isHealth", cue.BoolKind, "a bool: whether it is healthy"
	case "customStatus":
		want, kind, what = "message", cue.StringKind, "a string: the message shown"
	default:
		return nil
	}
	declared := topLevelField(file.Decls, want)
	if declared == nil {
		return []Diagnostic{d.at(f.label.Label.Pos(), fmt.Sprintf("%s must set %s, %s", f.name, want, what))}
	}
	if got := v.LookupPath(cue.ParsePath(want)); got.Exists() && got.Err() == nil && got.IncompleteKind()&kind == 0 {
		return []Diagnostic{d.statusDiag(f, declared.Label.Pos(), fmt.Sprintf("%s: %s must be %s", f.name, want, what))}
	}
	if onlyAType(declared.Value) {
		return []Diagnostic{d.statusDiag(f, declared.Label.Pos(), fmt.Sprintf("%s: %s is only a type, so it never evaluates: give it a value, %s", f.name, want, what))}
	}
	return nil
}

// maxDetailLabel is the length from which KubeVela skips a detail's label.
const maxDetailLabel = 32

// checkDetails warns of the details KubeVela will not show as written: a
// label too long, which it skips, and a value that is only a type, which it
// shows as _|_. A detail named context or parameter unifies with the
// runtime context's own, so CUE reports what conflicts.
func (d *document) checkDetails(f statusField, file *ast.File) []Diagnostic {
	var diags []Diagnostic
	warn := func(pos token.Pos, msg string) {
		diag := d.statusDiag(f, pos, "details: "+msg)
		diag.Severity = SeverityWarning
		diags = append(diags, diag)
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		label := labelName(fd.Label)
		if strings.HasPrefix(label, "$") || strings.HasPrefix(label, "_") || strings.HasPrefix(label, "#") || hasAttr(fd, "local", "private") {
			continue
		}
		switch {
		case len(label) >= maxDetailLabel:
			warn(fd.Label.Pos(), fmt.Sprintf("%s is %d characters: KubeVela skips a label of %d or more", label, len(label), maxDetailLabel))
		case onlyAType(fd.Value):
			warn(fd.Label.Pos(), fmt.Sprintf("%s is only a type, so KubeVela shows it as _|_: give it a value", label))
		}
	}
	return diags
}

// hasAttr reports whether a field carries an attribute of one of names.
func hasAttr(f *ast.Field, names ...string) bool {
	for _, a := range f.Attrs {
		for _, n := range names {
			if strings.HasPrefix(a.Text, "@"+n+"(") || a.Text == "@"+n {
				return true
			}
		}
	}
	return false
}

// cueTypes are CUE's types, which a field set to alone never evaluates.
var cueTypes = map[string]bool{"string": true, "int": true, "bool": true, "number": true, "float": true, "bytes": true, "_": true, "uint": true}

// onlyAType reports whether e is a type alone, as in int, and no value.
func onlyAType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && cueTypes[id.Name] && id.Node == nil
}

// statusFieldAt is the status field the offset is in, and the offset in its
// text.
func statusFieldAt(path, doc string, offset int, opts Options) (*document, statusField, int, bool) {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return nil, statusField{}, 0, false
	}
	d, ok := newDocument(path, []byte(doc), f)
	if !ok || (d.typ != componentType && d.typ != traitType) {
		return nil, statusField{}, 0, false
	}
	d.opts = opts
	for _, sf := range d.statusFields() {
		if at, ok := sf.offsetIn(d.src, offset); ok {
			return d, sf, at, true
		}
	}
	return nil, statusField{}, 0, false
}

// CompleteStatusField completes in a status field: a field of context, the
// live output by its kind, of parameter, or of a value the field declares.
// It is false when the offset is in no status field.
func CompleteStatusField(path, doc string, offset int, opts Options) ([]Completion, bool) {
	d, sf, at, ok := statusFieldAt(path, doc, offset, opts)
	if !ok {
		return nil, false
	}
	before := sf.text[:at]
	m := valueTyped.FindStringSubmatchIndex(before)
	if m == nil {
		return nil, true
	}
	root, typed := before[m[2]:m[3]], before[m[6]:m[7]]
	var chain []string
	if c := strings.TrimPrefix(before[m[4]:m[5]], "."); c != "" {
		chain = strings.Split(c, ".")
	}
	// context and parameter do not depend on the field's own text, which,
	// half typed, may not evaluate; its own values need it, with a
	// placeholder for the reference being typed.
	text := ""
	if root != contextLabel && root != parameterLabel {
		text = rootOf(sf.text, m[2], at)
	}
	_, v, param, err := d.statusValue(sf, text)
	if err != nil || !v.Exists() {
		return nil, true
	}
	var cur cue.Value
	switch root {
	case parameterLabel:
		cur = param
	default:
		cur = schemaChild(v, cue.Str(root))
	}
	out := fieldsOf(walk(cur, chain), typed)
	if root == contextLabel && len(chain) == 0 {
		out = withoutHidden(out, d.typ)
	}
	return out, true
}

// withoutHidden drops the context fields not offered from completions of
// context's own fields.
func withoutHidden(cs []Completion, defType string) []Completion {
	hidden := hiddenContext(defType)
	out := cs[:0]
	for _, c := range cs {
		if !hidden[c.Label] {
			out = append(out, c)
		}
	}
	return out
}

// offeredContext is names without the context fields not offered.
func offeredContext(names []string, defType string) []string {
	hidden := hiddenContext(defType)
	var out []string
	for _, n := range names {
		if !hidden[n] {
			out = append(out, n)
		}
	}
	return out
}

// hiddenContext is the set of a type's context fields not offered.
func hiddenContext(defType string) map[string]bool {
	hidden := map[string]bool{}
	for _, f := range ContextFields(defType) {
		hidden[f.Name] = f.Hidden
	}
	return hidden
}

// HoverStatusField describes the field read at offset in a status field.
func HoverStatusField(path, doc string, offset int, opts Options) (string, bool) {
	if offset < 0 || offset >= len(doc) || !isWordByteAt(doc[offset]) {
		return "", false
	}
	start, end := offset, wordEnd(doc, offset)
	for start > 0 && isWordByteAt(doc[start-1]) {
		start--
	}
	got, ok := CompleteStatusField(path, doc, end, opts)
	if !ok {
		return "", false
	}
	for _, c := range got {
		if c.Label == doc[start:end] {
			return hoverText(c.Label, c.Detail, c.Doc), true
		}
	}
	return "", false
}

// topLevelField is the field of name a file declares at its top, written
// there or in an if or for block there.
func topLevelField(decls []ast.Decl, name string) *ast.Field {
	for _, decl := range decls {
		switch x := decl.(type) {
		case *ast.Field:
			if labelName(x.Label) == name {
				return x
			}
		case *ast.Comprehension:
			if s, ok := x.Value.(*ast.StructLit); ok {
				if f := topLevelField(s.Elts, name); f != nil {
					return f
				}
			}
		case *ast.EmbedDecl:
			if s, ok := x.Expr.(*ast.StructLit); ok {
				if f := topLevelField(s.Elts, name); f != nil {
					return f
				}
			}
		}
	}
	return nil
}

// DeclarationInStatusField is where a name read in a status field is
// declared: a field of the status field itself, or, for parameter and
// context.parameter, the template's parameter.
func DeclarationInStatusField(path, doc string, offset int) (Location, bool) {
	d, sf, at, ok := statusFieldAt(path, doc, offset, Options{})
	if !ok {
		return Location{}, false
	}
	ix, ok := parseNav(d.statusName(sf), sf.text)
	if !ok {
		return Location{}, false
	}
	n := ix.nodeAt(at)
	id, ok := n.(*ast.Ident)
	if !ok {
		return Location{}, false
	}
	root, labels := id, []string(nil)
	if c, ok := ix.chainOf[id]; ok {
		root, labels = c.root, c.labels
	}
	if root.Name == contextLabel && len(labels) > 0 && labels[0] == parameterLabel {
		labels = labels[1:]
	} else if root.Name != parameterLabel {
		target, ok := ix.target(at, nil)
		if !ok || target.ix != ix {
			return Location{}, false
		}
		l := target.location()
		start, end := sf.atPosition(l.Range.Start), sf.atPosition(l.Range.End)
		return Location{Path: path, Range: Range{Start: start, End: end}}, true
	}
	param, ok := fieldIn(d.template, parameterLabel)
	if !ok {
		return Location{}, false
	}
	for _, l := range labels {
		next, ok := fieldIn(param, l)
		if !ok {
			break
		}
		param = next
	}
	return Location{Path: path, Range: span(param.Label.Pos(), param.Label.End())}, true
}
