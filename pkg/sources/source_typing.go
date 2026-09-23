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
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/kubevela/workflow/pkg/cue/process"

	"github.com/oam-dev/kubevela/pkg/definition/celexpr"
	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

// cueType is a CUE type expression standing in for a value that is not knowable
// until render: "int", "string", "[...string]".
//
// A type and not a placeholder value, because CUE accepts both and only one is
// right. `replicas: int` unifies with a `>0 & int` constraint; a sentinel 0 is
// refused as out of bound, so it would turn a valid Application into a rejected
// one.
// typeOnlyKey marks a render as a validation rather than a real one.
type typeOnlyKey struct{}

// WithTypeOnly marks a context as validating, so an expression is typed from its
// source schema rather than resolved.
//
// Set once, on the whole validation, rather than at each render: admission
// renders the component and every trait, and a site that forgot would quietly
// perform the live I/O the rest of them avoid.
func WithTypeOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, typeOnlyKey{}, true)
}

// TypeOnly reports whether this render is a validation.
func TypeOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(typeOnlyKey{}).(bool)
	return on
}

// CUEType is a CUE type expression standing in for a value that is not knowable
// until render: "int", "string", "[...string]".
//
// A type and not a placeholder value, because CUE accepts both and only one is
// right. `replicas: int` unifies with a `>0 & int` constraint; a sentinel 0 is
// refused as out of bound, so it would turn a valid Application into a rejected
// one.
type CUEType string

// TypedParams replaces every $( ) expression in a component's properties with
// the type its source schema declares, leaving every other value alone.
//
// A struct-valued read keeps its shape rather than collapsing to one key. The
// required-field check flattens what was provided and looks for "meta.region",
// so a read of `meta` has to arrive as a map with the fields the schema gives
// it or it satisfies nothing.
func TypedParams(ctx process.Context, params map[string]any) (map[string]any, error) {
	if len(params) == 0 {
		return params, nil
	}
	schemas := SchemasForContext(ctx)
	t := &paramTyper{
		schemas:  schemas,
		compiled: map[string]cue.Value{},
		cuectx:   cuecontext.New(),
	}
	out, err := t.walk(params)
	if err != nil {
		return nil, err
	}
	typed, ok := out.(map[string]any)
	if !ok {
		return params, nil
	}
	return typed, nil
}

type paramTyper struct {
	schemas  map[string]string
	compiled map[string]cue.Value
	cuectx   *cue.Context
}

func (t *paramTyper) walk(node any) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			typed, err := t.walk(child)
			if err != nil {
				return nil, err
			}
			out[key] = typed
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			typed, err := t.walk(child)
			if err != nil {
				return nil, err
			}
			out[i] = typed
		}
		return out, nil
	case string:
		return t.typeOfLeaf(v)
	default:
		return node, nil
	}
}

// typeOfLeaf returns what a string leaf should contribute: itself when it holds
// no expression, otherwise the type the expression produces.
func (t *paramTyper) typeOfLeaf(raw string) (any, error) {
	parsed, err := propexpr.Parse(raw)
	if err != nil || !parsed.HasExpr() {
		//nolint:nilerr // a malformed expression is reported by the expression validator
		return raw, nil
	}
	expr, whole := parsed.SoleExpr()
	if !whole {
		// Interpolated into text, so the result is a string whatever the parts
		// are. celexpr refuses a struct or list in that position separately.
		return CUEType("string"), nil
	}
	// A plain read of a declared field - the common case - is typed from the
	// schema subtree, which carries the field's shape and not just its kind.
	if shaped, ok := t.fromSchema(expr); ok {
		return shaped, nil
	}
	// Anything computed: a ternary, arithmetic, a function call. CEL knows the
	// result type without knowing the values.
	return t.fromCEL(expr)
}

// fromSchema types a plain `source.<binding>.<path>` read from the binding's
// declared schema. Reports false for anything else, including a read of a
// binding with no schema to judge by.
func (t *paramTyper) fromSchema(expr string) (any, bool) {
	refs, err := celexpr.PropertyReferences(expr)
	if err != nil || len(refs) != 1 {
		return nil, false
	}
	ref := refs[0]
	if !ref.IsSource() || len(ref.Path) < 2 || ref.String() != expr {
		// Not a bare read: the expression does something with the value.
		return nil, false
	}
	schema, ok := t.schemaFor(ref.Path[0])
	if !ok {
		return nil, false
	}
	field := schema
	for _, segment := range ref.Path[1:] {
		field = field.LookupPath(cue.MakePath(cue.Str(segment)))
		if !field.Exists() {
			// The read is refused by the schema check; nothing to type here.
			return nil, false
		}
	}
	return t.shapeOf(field), true
}

// shapeOf converts a schema field into what typedParams emits: a nested map for
// a struct, so its fields survive flattening, and a type expression otherwise.
func (t *paramTyper) shapeOf(field cue.Value) any {
	if field.IncompleteKind() == cue.StructKind {
		iter, err := field.Fields(cue.Optional(true), cue.Definitions(false))
		if err != nil {
			return CUEType("{...}")
		}
		out := map[string]any{}
		for iter.Next() {
			sel := iter.Selector()
			if !sel.IsString() {
				continue
			}
			out[sel.Unquoted()] = t.shapeOf(iter.Value())
		}
		if len(out) == 0 {
			// An open map declares a value type and no keys, so there is no
			// shape to carry - only that anything here is allowed.
			return CUEType("{...}")
		}
		return out
	}
	return CUEType(kindExpr(field.IncompleteKind(), field))
}

// fromCEL types a computed expression from its output type alone.
func (t *paramTyper) fromCEL(expr string) (any, error) {
	env, err := celexpr.EnvForContext(t.schemas, propexpr.ComponentContext)
	if err != nil {
		return nil, err
	}
	out, err := celexpr.OutputType(env, expr)
	if err != nil {
		// The expression validator reports this properly. Leaving it untyped
		// here keeps one failure to one message.
		//nolint:nilerr // reported elsewhere
		return CUEType("_"), nil
	}
	return CUEType(celTypeExpr(out.String())), nil
}

func (t *paramTyper) schemaFor(binding string) (cue.Value, bool) {
	if v, ok := t.compiled[binding]; ok {
		return v, v.Exists()
	}
	text, ok := t.schemas[binding]
	if !ok {
		t.compiled[binding] = cue.Value{}
		return cue.Value{}, false
	}
	v := t.cuectx.CompileString(text)
	if v.Err() != nil {
		v = cue.Value{}
	}
	t.compiled[binding] = v
	return v, v.Exists()
}

// kindExpr names a CUE kind as a type expression.
//
// The kind and not the field's own syntax: a schema may constrain a value
// (`replicas: >0 & int`), and that is the source's constraint to enforce at
// render, not one to impose on whatever consumes it here.
func kindExpr(k cue.Kind, field cue.Value) string {
	switch k {
	case cue.StringKind:
		return "string"
	case cue.IntKind:
		return "int"
	case cue.FloatKind:
		return "float"
	case cue.NumberKind:
		return "number"
	case cue.BoolKind:
		return "bool"
	case cue.ListKind:
		if elem := field.LookupPath(cue.MakePath(cue.AnyIndex)); elem.Exists() {
			return "[..." + kindExpr(elem.IncompleteKind(), elem) + "]"
		}
		return "[...]"
	case cue.NullKind:
		return "null"
	}
	return "_"
}

// celTypeExpr names a CEL type as a CUE type expression.
func celTypeExpr(t string) string {
	switch t {
	case "string":
		return "string"
	case "int", "uint":
		return "int"
	case "double":
		return "float"
	case "bool":
		return "bool"
	case "bytes":
		return "bytes"
	case "null_type":
		return "null"
	}
	if strings.HasPrefix(t, "list(") {
		return "[..." + celTypeExpr(strings.TrimSuffix(strings.TrimPrefix(t, "list("), ")")) + "]"
	}
	if strings.HasPrefix(t, "map(") {
		return "{...}"
	}
	// dyn, any, a message type: nothing precise to say, so constrain nothing.
	return "_"
}

// ParamsAsCUE renders resolved-or-typed properties as CUE.
//
// Properties with no type marker in them are marshalled as JSON, which is valid
// CUE and byte-identical to what every caller produced before typing existed. A
// marker cannot survive JSON - it is a type, not a value - so only those take
// the slower path.
func ParamsAsCUE(params map[string]any) (string, error) {
	if !hasCUEType(params) {
		raw, err := json.Marshal(params)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	return renderCUE(params)
}

// hasCUEType reports whether anything in the tree is a type rather than a value.
func hasCUEType(node any) bool {
	switch v := node.(type) {
	case CUEType:
		return true
	case map[string]any:
		for _, child := range v {
			if hasCUEType(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasCUEType(child) {
				return true
			}
		}
	}
	return false
}

// renderCUE writes a value as CUE. JSON is valid CUE, so only the type markers
// need special handling: they are written bare rather than quoted.
func renderCUE(node any) (string, error) {
	switch v := node.(type) {
	case CUEType:
		return string(v), nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			rendered, err := renderCUE(v[key])
			if err != nil {
				return "", err
			}
			label, err := json.Marshal(key)
			if err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("%s: %s", label, rendered))
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case []any:
		parts := make([]string, 0, len(v))
		for _, child := range v {
			rendered, err := renderCUE(child)
			if err != nil {
				return "", err
			}
			parts = append(parts, rendered)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		raw, err := json.Marshal(node)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

// ConcreteForValidation fills the leaves a typed render left non-concrete, so
// the result can be marshalled.
//
// A typed parameter makes whatever it feeds non-concrete, and the rendered
// output of a component becomes the base the trait renders against - which the
// workflow engine passes as JSON, and JSON has no way to spell "some string".
//
// Filling here and not in the parameters is the distinction that matters. The
// parameter block is checked against the definition's constraints, where a zero
// value is a wrong answer: it would refuse `replicas: >0` outright. The output
// is checked against nothing; it only has to exist so the trait template can be
// evaluated. A value that was never knowable at admission is arbitrary either
// way - before this, it was whatever the source happened to return.
func ConcreteForValidation(v cue.Value) (cue.Value, bool) {
	filled, changed := fillIncomplete(v)
	if !changed {
		return v, false
	}
	out := v.Context().Encode(filled)
	if out.Err() != nil {
		return v, false
	}
	return out, true
}

func fillIncomplete(v cue.Value) (any, bool) {
	switch v.IncompleteKind() {
	case cue.StructKind:
		iter, err := v.Fields()
		if err != nil {
			return map[string]any{}, false
		}
		out := map[string]any{}
		changed := false
		for iter.Next() {
			child, childChanged := fillIncomplete(iter.Value())
			out[iter.Selector().String()] = child
			changed = changed || childChanged
		}
		return out, changed
	case cue.ListKind:
		iter, err := v.List()
		if err != nil {
			return []any{}, false
		}
		out := []any{}
		changed := false
		for iter.Next() {
			child, childChanged := fillIncomplete(iter.Value())
			out = append(out, child)
			changed = changed || childChanged
		}
		return out, changed
	}
	if v.IsConcrete() {
		var decoded any
		if err := v.Decode(&decoded); err == nil {
			return decoded, false
		}
	}
	return zeroOf(v.IncompleteKind()), true
}

// zeroOf is a stand-in of the right shape for a value that is not knowable.
func zeroOf(k cue.Kind) any {
	switch k {
	case cue.StringKind:
		return ""
	case cue.IntKind:
		return 0
	case cue.FloatKind, cue.NumberKind:
		return 0.0
	case cue.BoolKind:
		return false
	case cue.ListKind:
		return []any{}
	case cue.StructKind:
		return map[string]any{}
	}
	return ""
}
