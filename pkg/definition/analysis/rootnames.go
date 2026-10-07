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
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

var (
	// nameTyped is a name begun at the end of the text before the cursor,
	// after what precedes a reference: an operator, a bracket or a colon, or
	// nothing but the line's indentation.
	nameTyped = regexp.MustCompile(`(?:^\s*|[:&|(,\[+\-*/=!<>]\s*)([A-Za-z_#$][A-Za-z0-9_#$]*)$`)
	// importBlock is an import declaration, single or a block; importLine one
	// of its specs, with its name if any.
	importBlock = regexp.MustCompile(`(?m)^import\s*(\([^)]*\)|"[^"]*"|[A-Za-z_][A-Za-z0-9_]*\s+"[^"]*")`)
	importLine  = regexp.MustCompile(`(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?"([^"]+)"`)
	// declaredName is a helper, a definition or a let the file declares.
	declaredName = regexp.MustCompile(`(?m)^\s*(?:(_[A-Za-z0-9_]*|#[A-Za-z0-9_]+)\??:|let\s+([A-Za-z_][A-Za-z0-9_]*)\s*=)`)
)

// CompleteRootName completes a reference's first name where one is begun at
// cursor in doc: context, parameter, the file's imports by their names, and
// the names in scope there. A name alone on a line is completed too, as it
// may be an expression embedded there. Nothing is offered after a dot, or
// before a name is begun.
func CompleteRootName(doc string, cursor int) []Completion {
	lineStart := strings.LastIndex(doc[:cursor], "\n") + 1
	line := doc[lineStart:cursor]
	m := nameTyped.FindStringSubmatchIndex(line)
	if m == nil {
		return nil
	}
	typed := line[m[2]:m[3]]
	var out []Completion
	add := func(name, detail, doc string) {
		if strings.HasPrefix(name, typed) {
			for _, c := range out {
				if c.Label == name {
					return
				}
			}
			out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: detail, Doc: doc})
		}
	}
	add(contextLabel, "context", "What KubeVela gives the template at render: the application's and component's names, namespace, cluster and more.")
	add(parameterLabel, "parameter", "The parameters the template declares, as the user gives them.")
	for _, block := range importBlock.FindAllStringSubmatch(doc, -1) {
		for _, spec := range importLine.FindAllStringSubmatch(block[1], -1) {
			name := spec[1]
			if name == "" {
				name = spec[2][strings.LastIndex(spec[2], "/")+1:]
			}
			add(name, "import", "The "+spec[2]+" package.")
		}
	}
	start := lineStart + m[2]
	if names, ok := namesInScope(doc[:start] + cursorPlaceholder + doc[cursor:]); ok {
		for _, n := range names {
			add(n.name, n.detail, "")
		}
		return out
	}
	for _, d := range declaredName.FindAllStringSubmatch(doc, -1) {
		switch {
		case d[1] != "":
			add(d[1], "declared here", "")
		case d[2] != "":
			add(d[2], "let", "")
		}
	}
	return out
}

// scopeName is a name a reference at the cursor can start with.
type scopeName struct{ name, detail string }

// namesInScope are the names a reference where the placeholder stands in
// doc can start with, innermost first: the fields of each struct enclosing
// it, its lets, and the variables of each for enclosing it; not the field
// whose value it is in, as a field referring to itself is a cycle. False
// when doc does not parse.
func namesInScope(doc string) ([]scopeName, bool) {
	f, err := parser.ParseFile("scope.cue", doc)
	if err != nil {
		return nil, false
	}
	var stack, found []ast.Node
	labels := map[ast.Node]bool{}
	ast.Walk(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if fd, ok := n.(*ast.Field); ok {
			labels[fd.Label] = true
		}
		stack = append(stack, n)
		if id, ok := n.(*ast.Ident); ok && id.Name == cursorPlaceholder && !labels[id] {
			found = append([]ast.Node{}, stack...)
			return false
		}
		return true
	}, func(ast.Node) { stack = stack[:len(stack)-1] })
	if found == nil {
		return nil, false
	}
	// The field the cursor's value belongs to, at each level.
	own := map[*ast.Field]bool{}
	for _, n := range found {
		if fd, ok := n.(*ast.Field); ok {
			own[fd] = true
		}
	}
	var out []scopeName
	seen := map[string]bool{}
	add := func(name, detail string) {
		if name != "" && !seen[name] && ast.IsValidIdent(name) && name != cursorPlaceholder {
			seen[name] = true
			out = append(out, scopeName{name, detail})
		}
	}
	for i := len(found) - 1; i >= 0; i-- {
		switch x := found[i].(type) {
		case *ast.StructLit:
			for _, e := range x.Elts {
				switch el := e.(type) {
				case *ast.Field:
					if !own[el] {
						add(labelName(el.Label), "field")
					}
				case *ast.LetClause:
					add(el.Ident.Name, "let")
				}
			}
		case *ast.File:
			for _, e := range x.Decls {
				if el, ok := e.(*ast.Field); ok && !own[el] && strings.HasPrefix(labelName(el.Label), "#") {
					add(labelName(el.Label), "definition")
				}
			}
		case *ast.Comprehension:
			for _, cl := range x.Clauses {
				switch c := cl.(type) {
				case *ast.ForClause:
					if c.Key != nil {
						add(c.Key.Name, "for key")
					}
					add(c.Value.Name, "for value")
				case *ast.LetClause:
					add(c.Ident.Name, "let")
				}
			}
		}
	}
	return out, true
}
