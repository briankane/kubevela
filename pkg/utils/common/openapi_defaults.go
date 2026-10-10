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

package common

import (
	"encoding/json"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"
	"cuelang.org/go/encoding/openapi"
)

// boundedDefault is a default taken off a field for CUE's OpenAPI encoder,
// which rejects one beside bounds (*1 | int & >=1 & <=9: "unsupported op for
// number &"), and where to set it in the schema made without it.
type boundedDefault struct {
	path  []string
	value ast.Expr
}

// genWithoutBoundedDefaults is the OpenAPI schema of v made with each field's
// default beside a constraint taken off, then set in the schema, or false
// when there is none to take off or the schema still cannot be made.
func genWithoutBoundedDefaults(v cue.Value, cfg *openapi.Config) ([]byte, bool) {
	var file *ast.File
	switch n := v.Syntax(cue.Docs(true), cue.Optional(true), cue.Definitions(true)).(type) {
	case *ast.File:
		file = n
	case *ast.StructLit:
		file = &ast.File{Decls: n.Elts}
	default:
		return nil, false
	}
	var defaults []boundedDefault
	var root string
	for _, d := range file.Decls {
		f, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		name, _, err := ast.LabelName(f.Label)
		if err != nil {
			continue
		}
		root = name
		f.Value = takeDefaults(f.Value, nil, &defaults)
	}
	if len(defaults) == 0 {
		return nil, false
	}
	stripped := v.Context().BuildFile(file)
	if stripped.Err() != nil {
		return nil, false
	}
	b, err := openapi.Gen(stripped, cfg)
	if err != nil {
		return nil, false
	}
	var doc map[string]interface{}
	if json.Unmarshal(b, &doc) != nil {
		return nil, false
	}
	schemas, _ := doc["components"].(map[string]interface{})["schemas"].(map[string]interface{})
	param, ok := schemas[trimDef(root)].(map[string]interface{})
	if !ok {
		return nil, false
	}
	for _, d := range defaults {
		at := param
		for _, seg := range d.path {
			next, ok := at[seg].(map[string]interface{})
			if !ok {
				at = nil
				break
			}
			at = next
		}
		lit := v.Context().BuildExpr(d.value)
		var value interface{}
		if at == nil || lit.Decode(&value) != nil {
			continue
		}
		at["default"] = value
	}
	out, err := json.Marshal(doc)
	return out, err == nil
}

// trimDef is a definition's name as the encoder names its schema: #parameter is parameter.
func trimDef(name string) string {
	if len(name) > 0 && name[0] == '#' {
		return name[1:]
	}
	return name
}

// takeDefaults is e with the default of each field under it that sits beside
// a constraint taken off, each recorded with its path in the schema: under
// properties for a struct's field, under items for a list's elements.
func takeDefaults(e ast.Expr, path []string, out *[]boundedDefault) ast.Expr {
	switch x := e.(type) {
	case *ast.StructLit:
		for _, d := range x.Elts {
			f, ok := d.(*ast.Field)
			if !ok {
				continue
			}
			name, _, err := ast.LabelName(f.Label)
			if err != nil {
				continue
			}
			f.Value = takeDefaults(f.Value, append(append([]string{}, path...), "properties", name), out)
		}
		return x
	case *ast.ListLit:
		for _, d := range x.Elts {
			if el, ok := d.(*ast.Ellipsis); ok && el.Type != nil {
				el.Type = takeDefaults(el.Type, append(append([]string{}, path...), "items"), out)
			}
		}
		return x
	case *ast.BinaryExpr:
		if x.Op != token.OR {
			return x
		}
		var def ast.Expr
		var rest []ast.Expr
		for _, d := range disjuncts(x) {
			if u, ok := d.(*ast.UnaryExpr); ok && u.Op == token.MUL && def == nil {
				def = u.X
				continue
			}
			rest = append(rest, d)
		}
		// Only a default beside one constraint, which carries what the field takes.
		if def == nil || len(rest) != 1 || !isConjunction(rest[0]) {
			return x
		}
		*out = append(*out, boundedDefault{path: path, value: def})
		return rest[0]
	}
	return e
}

// disjuncts are the operands of a | b | c.
func disjuncts(e ast.Expr) []ast.Expr {
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.OR {
		return append(disjuncts(b.X), disjuncts(b.Y)...)
	}
	if p, ok := e.(*ast.ParenExpr); ok {
		return disjuncts(p.X)
	}
	return []ast.Expr{e}
}

// isConjunction is whether e is a & b.
func isConjunction(e ast.Expr) bool {
	if p, ok := e.(*ast.ParenExpr); ok {
		return isConjunction(p.X)
	}
	b, ok := e.(*ast.BinaryExpr)
	return ok && b.Op == token.AND
}

// RefusedByEncoderAlone is whether CUE's OpenAPI encoder on its own refuses
// v's parameter, as KubeVela releases without the bounded-default fallback do,
// while GenOpenAPI makes its schema.
func RefusedByEncoderAlone(v cue.Value) bool {
	param, err := RefineParameterValue(v)
	if err != nil {
		return false
	}
	cfg := &openapi.Config{ExpandReferences: true}
	if _, err := openapi.Gen(param, cfg); err == nil {
		return false
	}
	_, ok := genWithoutBoundedDefaults(param, cfg)
	return ok
}
