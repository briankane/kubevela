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
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

// InlayHint is a label shown in the text at a position.
type InlayHint struct {
	Position Position
	Label    string
	Tooltip  string
}

// maxHintLen bounds a default shown inline; a longer one is left to hover.
const maxHintLen = 40

// InlayHints are the defaults of the parameters a definition reads, each
// shown after a read of it: "= 1".
func InlayHints(path, doc string, opts Options) []InlayHint {
	f, err := parser.ParseFile(path, doc, parser.ParseComments)
	if err != nil {
		return nil
	}
	d, ok := newDocument(path, []byte(doc), f)
	if !ok {
		return nil
	}
	d.opts = opts
	// Evaluation rewrites the template's syntax, so the reads are taken
	// from a parse of their own.
	reads, err := parser.ParseFile(path, doc)
	if err != nil {
		return nil
	}
	v, ok := d.evaluate()
	if !ok {
		return nil
	}
	params := v.LookupPath(cue.MakePath(cue.Def(closedParameterPath)))
	if !params.Exists() {
		return nil
	}
	var hints []InlayHint
	ast.Walk(reads, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root, chain := flatten(sel)
		if root == nil || root.Name != parameterLabel {
			return false
		}
		cur := params
		for _, id := range chain {
			cur = schemaChild(cur, cue.Str(id.Name))
		}
		def, ok := cur.Default()
		if !ok || !def.IsConcrete() || def.IncompleteKind() == cue.StructKind {
			return false
		}
		b, err := def.MarshalJSON()
		if err != nil || len(b) > maxHintLen {
			return false
		}
		end := sel.End()
		hints = append(hints, InlayHint{
			Position: Position{Line: end.Line(), Column: end.Column()},
			Label:    "= " + string(b),
			Tooltip:  "The default of parameter." + chainString(chain),
		})
		return false
	}, nil)
	return hints
}

func chainString(chain []*ast.Ident) string {
	out := ""
	for i, id := range chain {
		if i > 0 {
			out += "."
		}
		out += id.Name
	}
	return out
}
