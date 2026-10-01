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

package sources

import (
	"fmt"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
)

// DefinitionAutoUpdate reads a SourceDefinition template's storage.autoUpdate:
// whether the source's data is live by nature, so a change to it re-dispatches
// what reads it. nil means the template says nothing, leaving it to the
// binding or the EnableSourceAutoUpdate gate.
//
// The controller decides before anything renders, so the value is read from
// the source text and must be a literal true or false.
func DefinitionAutoUpdate(template string) (*bool, error) {
	f, err := parser.ParseFile("-", template)
	if err != nil {
		return nil, err
	}
	storage := fieldValue(f.Decls, "storage")
	if storage == nil {
		return nil, nil
	}
	s, ok := storage.(*ast.StructLit)
	if !ok {
		return nil, nil
	}
	v := fieldValue(s.Elts, "autoUpdate")
	if v == nil {
		return nil, nil
	}
	if ident, ok := v.(*ast.Ident); ok && (ident.Name == "true" || ident.Name == "false") {
		on := ident.Name == "true"
		return &on, nil
	}
	if lit, ok := v.(*ast.BasicLit); ok && (lit.Value == "true" || lit.Value == "false") {
		on := lit.Value == "true"
		return &on, nil
	}
	return nil, fmt.Errorf("storage.autoUpdate must be a literal true or false")
}

// fieldValue is the value of the named field among a struct's declarations.
func fieldValue(decls []ast.Decl, name string) ast.Expr {
	for _, d := range decls {
		field, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		label, _, err := ast.LabelName(field.Label)
		if err != nil || label != name {
			continue
		}
		return field.Value
	}
	return nil
}
