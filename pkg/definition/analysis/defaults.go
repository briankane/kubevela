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
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"
)

// checkDefaultsThatWin warns of a disjunction of a value read and a literal
// marked default, as in parameter.image | *"nginx": both sides are
// concrete when the value is set, so the disjunction is ambiguous and its
// default, the literal, wins. Marking the read as the default, as in
// *parameter.image | "nginx", is what is meant.
func (d *document) checkDefaultsThatWin() []Diagnostic {
	if d.template == nil {
		return nil
	}
	var diags []Diagnostic
	ast.Walk(d.template.Value, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok || b.Op != token.OR {
			return true
		}
		read, lit := b.X, b.Y
		def, ok := lit.(*ast.UnaryExpr)
		if !ok || def.Op != token.MUL || !isLiteral(def.X) || !isValueRead(read) {
			return true
		}
		readText, litText := d.sourceOf(read), d.sourceOf(def.X)
		diag := d.at(b.Pos(), fmt.Sprintf("the default wins here even when %s is set: a disjunction of two concrete values takes its default. Mark the read as the default instead: *%s | %s", readText, readText, litText))
		diag.Severity = SeverityWarning
		to := "*" + readText + " | " + litText
		diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: span(b.Pos(), b.End()), NewText: to}}}}
		diags = append(diags, diag)
		return false
	}, nil)
	return diags
}

// isLiteral reports whether e is a literal value: a string, number, bool or
// null, or a struct or list of literals.
func isLiteral(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Interpolation:
		return true
	case *ast.ListLit:
		for _, el := range x.Elts {
			if !isLiteral(el) {
				return false
			}
		}
		return true
	case *ast.StructLit:
		for _, el := range x.Elts {
			f, ok := el.(*ast.Field)
			if !ok || !isLiteral(f.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// isValueRead reports whether e reads a value: parameter, context or a
// field of the template, not a type or a definition.
func isValueRead(e ast.Expr) bool {
	var root *ast.Ident
	switch x := e.(type) {
	case *ast.Ident:
		root = x
	case *ast.SelectorExpr:
		var chain []*ast.Ident
		root, chain = flatten(x)
		if root == nil || (len(chain) > 0 && strings.HasPrefix(chain[len(chain)-1].Name, "#")) {
			return false
		}
	case *ast.IndexExpr:
		return isValueRead(x.X)
	default:
		return false
	}
	if root.Name == parameterLabel || root.Name == contextLabel {
		return true
	}
	// A field declared in the file, not a type such as string, nor a
	// definition or an import.
	if strings.HasPrefix(root.Name, "#") {
		return false
	}
	switch root.Node.(type) {
	case nil, *ast.ImportSpec:
		return false
	}
	return true
}

// sourceOf is the source text of a node.
func (d *document) sourceOf(n ast.Node) string {
	start, end := n.Pos().Offset(), n.End().Offset()
	if start < 0 || end > len(d.src) || start > end {
		return ""
	}
	return string(d.src[start:end])
}
