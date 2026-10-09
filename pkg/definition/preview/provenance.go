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

package preview

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// Location is a field of what a definition renders: the object, "output",
// "outputs.<name>" or, for a trait, "workload", the field's path in it, each
// step a key or a list index, and the input of a values file of several.
type Location struct {
	Object string        `json:"object"`
	Path   []interface{} `json:"path"`
	Input  string        `json:"input,omitempty"`
}

// Origin is where a rendered field's value comes from in the template.
type Origin struct {
	// Kind is parameter, context, literal, expression, or workload for a
	// field of a trait's workload that its patch leaves alone.
	Kind string `json:"kind"`
	// Ref is the parameter or context field it is, for those kinds.
	Ref string `json:"ref,omitempty"`
	// Set is whether the values set the parameter, rather than its default.
	Set bool `json:"set,omitempty"`
	// Via are the template's own fields it passes through on the way.
	Via []string `json:"via,omitempty"`
	// Expression is the CUE that computes it, for an expression.
	Expression string `json:"expression,omitempty"`
	// Uses are the parameter and context fields an expression reads.
	Uses []string `json:"uses,omitempty"`
	// Line and Column are where the field is in the file, 0-based.
	Line   int `json:"line"`
	Column int `json:"column"`
	// RefLine is where the parameter is declared, 0-based, for a parameter.
	RefLine int `json:"refLine,omitempty"`
}

// maxVia bounds how many of the template's own fields a value is followed through.
const maxVia = 8

// Provenance says where the value of a field the definition in req renders
// comes from, with req.Values: the parameter or context field it reads,
// through which of the template's own fields, or the literal or expression
// that makes it, and where in the file each is.
func Provenance(_ context.Context, req Request, at Location) (Origin, error) {
	f, tmpl, ok := analysis.TemplateFile(req.Path, req.Source)
	if !ok {
		return Origin{}, fmt.Errorf("%s is not a definition, or does not parse", req.Path)
	}
	v, err := inputValues(req.Values, at.Input)
	if err != nil {
		return Origin{}, err
	}
	// The controller supplies context; the template only reads it.
	f.Decls = append(f.Decls, &ast.Field{Label: ast.NewIdent("context"), Value: &ast.StructLit{Elts: []ast.Decl{&ast.Ellipsis{}}}})
	root := cuecontext.New().BuildFile(f)
	if root.Err() != nil {
		return Origin{}, root.Err()
	}
	if v.Parameter != nil {
		root = root.FillPath(cue.ParsePath("parameter"), v.Parameter)
	}
	ctxFields := map[string]interface{}{
		"name":      v.Context.name(tmpl.Name),
		"appName":   orDefault(v.Context.AppName, defaultAppName),
		"namespace": orDefault(v.Context.Namespace, defaultNamespace),
		"cluster":   orDefault(v.Context.Cluster, defaultCluster),
	}
	if tmpl.Type == "trait" {
		workload := v.Workload
		if workload == nil {
			workload = sampleWorkload(v.Context.name(tmpl.Name))
		}
		ctxFields["output"] = workload
	}
	root = root.FillPath(cue.ParsePath("context"), ctxFields)

	base := at.Object
	if base == "workload" {
		base = "patch"
	}
	sels := []cue.Selector{}
	for _, s := range strings.Split(base, ".") {
		sels = append(sels, cue.Str(s))
	}
	object := len(sels)
	for _, p := range at.Path {
		switch x := p.(type) {
		case string:
			sels = append(sels, cue.Str(x))
		case float64:
			sels = append(sels, cue.Index(int(x)))
		case int:
			sels = append(sels, cue.Index(x))
		default:
			return Origin{}, fmt.Errorf("a path step is a key or an index, not %v", p)
		}
	}
	field := root.LookupPath(cue.MakePath(sels...))
	if !field.Exists() {
		if at.Object == "workload" {
			return Origin{Kind: "workload"}, nil
		}
		return Origin{}, fmt.Errorf("the template has no %s", cue.MakePath(sels...))
	}
	o := origin(root, field, v.Parameter, 0)
	// A struct the field is in may be one of the template's own fields, as
	// in labels: _labels; the field then comes through it.
	for i := len(sels) - 1; i > object; i-- {
		if _, p := root.LookupPath(cue.MakePath(sels[:i]...)).ReferencePath(); len(p.Selectors()) > 0 {
			o.Via = append([]string{cue.MakePath(append(p.Selectors(), sels[i:]...)...).String()}, o.Via...)
			break
		}
	}
	return o, nil
}

// inputValues is the values document named input, or the only one.
func inputValues(src []byte, input string) (values, error) {
	docs, err := splitDocuments(src)
	if err != nil {
		return values{}, fmt.Errorf("values: %w", err)
	}
	var v values
	if len(docs) == 0 {
		return v, nil
	}
	pick := docs[0]
	if input != "" {
		for i, d := range docs {
			var named values
			if yaml.Unmarshal(d, &named) == nil && (named.Name == input || strconv.Itoa(i+1) == input) {
				pick = d
				break
			}
		}
	}
	if err := yaml.Unmarshal(pick, &v); err != nil {
		return values{}, fmt.Errorf("values: %w", err)
	}
	return v, nil
}

// origin says where field's value comes from, following references to the
// template's own fields until it reaches a parameter, the context, or a value
// made in place.
func origin(root, field cue.Value, set map[string]interface{}, depth int) Origin {
	pos := field.Pos()
	o := Origin{Line: max(pos.Line()-1, 0), Column: max(pos.Column()-1, 0)}
	ref, path := field.ReferencePath()
	if ref.Exists() && len(path.Selectors()) > 0 {
		first := path.Selectors()[0].String()
		switch first {
		case "parameter":
			o.Kind, o.Ref, o.Set = "parameter", path.String(), isSet(set, path.Selectors()[1:])
			if decl := root.LookupPath(path).Pos(); decl.IsValid() {
				o.RefLine = decl.Line() - 1
			}
			return o
		case "context":
			o.Kind, o.Ref = "context", path.String()
			return o
		}
		if depth < maxVia {
			next := origin(root, ref.LookupPath(path), set, depth+1)
			next.Line, next.Column = o.Line, o.Column
			next.Via = append([]string{path.String()}, next.Via...)
			return next
		}
	}
	op, _ := field.Expr()
	if op == cue.NoOp && field.IsConcrete() && !hasReference(field) {
		o.Kind = "literal"
		return o
	}
	o.Kind = "expression"
	if src := field.Source(); src != nil {
		if expr := expressionOf(src); expr != nil {
			if b, err := format.Node(expr); err == nil {
				o.Expression = string(b)
			}
		}
	}
	o.Uses = uses(field)
	if o.Expression == "" && len(o.Uses) == 0 {
		o.Kind = "literal"
	}
	return o
}

// expressionOf is the value of a field node, or the node itself.
func expressionOf(n ast.Node) ast.Node {
	if f, ok := n.(*ast.Field); ok {
		return f.Value
	}
	return n
}

// hasReference is whether a value reads another field.
func hasReference(v cue.Value) bool {
	return len(uses(v)) > 0
}

// uses are the parameter and context fields a value's expression reads.
func uses(v cue.Value) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(cue.Value, int)
	walk = func(x cue.Value, depth int) {
		if depth > 4 {
			return
		}
		if _, p := x.ReferencePath(); len(p.Selectors()) > 0 {
			s := p.String()
			if first := p.Selectors()[0].String(); (first == "parameter" || first == "context") && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			return
		}
		_, args := x.Expr()
		for _, a := range args {
			walk(a, depth+1)
		}
	}
	_, args := v.Expr()
	for _, a := range args {
		walk(a, 0)
	}
	return out
}

// isSet is whether the values set the parameter at path.
func isSet(set map[string]interface{}, path []cue.Selector) bool {
	var cur interface{} = set
	for _, s := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return false
		}
		key := s.String()
		if unq, err := strconv.Unquote(key); err == nil {
			key = unq
		}
		if cur, ok = m[key]; !ok {
			return false
		}
	}
	return true
}
