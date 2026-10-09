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
	"strconv"
	"strings"
	"unicode"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// renderedRoots are the template fields whose literals become what renders.
var renderedRoots = map[string]bool{"output": true, "outputs": true, "patch": true}

// literalAt is a scalar literal a template field is set to, found at an offset.
type literalAt struct {
	label string
	// root names what it renders in: output, outputs.<name>, or patch.
	root string
	lit  *ast.BasicLit
}

// ParameterActions are the refactors offered at offset of a definition: on a
// scalar literal in what the template renders, make it a parameter with the
// literal as its default, or use a parameter of that name and type.
func ParameterActions(path, doc string, offset int) []Fix {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return nil
	}
	d, ok := newDocument(path, []byte(doc), f)
	if !ok {
		return nil
	}
	tmpl := d.template.Value.(*ast.StructLit)
	at, ok := findLiteral(tmpl, offset)
	if !ok {
		return nil
	}
	typ := literalType(at.lit)
	params := map[string]string{}
	var block *ast.StructLit
	for _, e := range tmpl.Elts {
		if fl, ok := e.(*ast.Field); ok && labelName(fl.Label) == "parameter" {
			if s, ok := fl.Value.(*ast.StructLit); ok {
				block = s
				for _, pe := range s.Elts {
					if pf, ok := pe.(*ast.Field); ok {
						params[labelName(pf.Label)] = declaredType(pf.Value)
					}
				}
			}
		}
	}
	name := parameterName(at.label)
	var fixes []Fix
	if t, taken := params[name]; taken {
		if t == typ {
			fixes = append(fixes, Fix{Title: "Use parameter." + name, Edits: []RangeEdit{{Range: span(at.lit.Pos(), at.lit.End()), NewText: "parameter." + name}}})
		}
		base := name
		for i := 2; ; i++ {
			name = base + strconv.Itoa(i)
			if _, taken := params[name]; !taken {
				break
			}
		}
	}
	usage := strings.ToUpper(name[:1]) + name[1:] + " of the " + at.root
	if strings.HasPrefix(at.root, "outputs.") {
		usage = strings.ToUpper(name[:1]) + name[1:] + " of " + at.root
	}
	decl := "// +usage=" + usage + "\n" + "%s" + name + ": *" + at.lit.Value + " | " + typ + "\n"
	var insert RangeEdit
	if block != nil {
		indent := d.lineIndent(block.Rbrace.Line()) + "\t"
		if strings.TrimSpace(sourceLine(doc, block.Rbrace.Line())[:block.Rbrace.Column()-1]) == "" {
			insert = RangeEdit{Range: Range{Start: Position{Line: block.Rbrace.Line(), Column: 1}, End: Position{Line: block.Rbrace.Line(), Column: 1}}, NewText: indent + strings.ReplaceAll(decl, "%s", indent)}
		} else {
			closing := d.lineIndent(block.Rbrace.Line())
			insert = RangeEdit{Range: span(block.Rbrace, block.Rbrace), NewText: "\n" + indent + strings.ReplaceAll(decl, "%s", indent) + closing}
		}
	} else {
		outer := d.lineIndent(tmpl.Lbrace.Line()) + "\t"
		inner := outer + "\t"
		line := tmpl.Lbrace.Line() + 1
		insert = RangeEdit{Range: Range{Start: Position{Line: line, Column: 1}, End: Position{Line: line, Column: 1}}, NewText: outer + "parameter: {\n" + inner + strings.ReplaceAll(decl, "%s", inner) + outer + "}\n"}
	}
	fixes = append(fixes, Fix{Title: "Make " + name + " a parameter", Edits: []RangeEdit{
		{Range: span(at.lit.Pos(), at.lit.End()), NewText: "parameter." + name},
		insert,
	}})
	return fixes
}

// findLiteral is the field set to a scalar literal at offset, under what the template renders.
func findLiteral(tmpl *ast.StructLit, offset int) (literalAt, bool) {
	var found literalAt
	var walk func(n ast.Node, label, root string) bool
	walk = func(n ast.Node, label, root string) bool {
		if n == nil || offset < n.Pos().Offset() || offset > n.End().Offset() {
			return false
		}
		switch x := n.(type) {
		case *ast.StructLit:
			for _, e := range x.Elts {
				if walk(e, label, root) {
					return true
				}
			}
		case *ast.ListLit:
			for _, e := range x.Elts {
				if walk(e, label, root) {
					return true
				}
			}
		case *ast.Field:
			name := labelName(x.Label)
			next := root
			switch {
			case root == "":
				if !renderedRoots[name] {
					return false
				}
				next = name
			case root == "outputs":
				next = "outputs." + name
			}
			if lit, ok := x.Value.(*ast.BasicLit); ok {
				if root == "" || literalType(lit) == "" {
					return false
				}
				found = literalAt{label: name, root: next, lit: lit}
				return true
			}
			return walk(x.Value, name, next)
		}
		return false
	}
	return found, walk(tmpl, "", "")
}

// literalType is the CUE type of a scalar literal, or "" for null and the like.
func literalType(lit *ast.BasicLit) string {
	switch lit.Kind {
	case token.INT:
		return "int"
	case token.FLOAT:
		return "number"
	case token.STRING:
		return "string"
	case token.TRUE, token.FALSE:
		return "bool"
	}
	return ""
}

// declaredType is the type a parameter is declared as, written as a type or
// as a default and a type; "" when it is anything else.
func declaredType(v ast.Expr) string {
	switch x := v.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.BinaryExpr:
		if x.Op == token.OR {
			if id, ok := x.Y.(*ast.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}

// parameterName is a name a reference can use, made from a field's label:
// its last path segment, in lower camel case.
func parameterName(label string) string {
	if i := strings.LastIndex(label, "/"); i >= 0 {
		label = label[i+1:]
	}
	var b strings.Builder
	upper := false
	for _, r := range label {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if upper && b.Len() > 0 {
				r = unicode.ToUpper(r)
			}
			b.WriteRune(r)
			upper = false
		default:
			upper = true
		}
	}
	name := b.String()
	if name == "" {
		return "value"
	}
	if unicode.IsDigit(rune(name[0])) {
		return "p" + name
	}
	return name
}

// sourceLine is a 1-based line of doc.
func sourceLine(doc string, line int) string {
	lines := strings.Split(doc, "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}
