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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// maxVariants bounds how many inputs an exploration renders.
const maxVariants = 50

// maxDepth bounds how deep into a parameter's structs an exploration looks.
const maxDepth = 3

// Variant is one input an exploration renders, and what it renders.
type Variant struct {
	// Name says how it differs from the values file, as "tier: \"batch\"".
	Name string `json:"name"`
	// Values is its parameter, YAML.
	Values   string   `json:"values"`
	Objects  []Object `json:"objects"`
	Workload *Object  `json:"workload,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Exploration is a definition rendered with its values file, the base, and
// with inputs that each differ from it in one parameter.
type Exploration struct {
	Base     Variant   `json:"base"`
	Variants []Variant `json:"variants"`
	// Truncated says there were more inputs than maxVariants.
	Truncated bool `json:"truncated,omitempty"`
}

// change is one parameter set, or unset, from the base.
type change struct {
	name  string
	path  []string
	value interface{}
	unset bool
}

// Explore renders the definition in req with the first input of req.Values,
// and with inputs that each change one parameter from it: an optional one
// unset or set, each value of a disjunction, a bool flipped, the bounds of a
// number. It is how a template's branches the values file never reaches are
// found.
func Explore(ctx context.Context, req Request) (Exploration, error) {
	f, tmpl, ok := analysis.TemplateFile(req.Path, req.Source)
	if !ok {
		return Exploration{}, fmt.Errorf("%s is not a definition, or does not parse", req.Path)
	}
	// The schema is read with no values, so a value given hides no default or bound.
	schema, _, err := buildTemplate(f, tmpl, nil, "")
	if err != nil {
		return Exploration{}, err
	}
	v, err := inputValues(req.Values, "")
	if err != nil {
		return Exploration{}, err
	}
	if v.Parameter == nil {
		v.Parameter = map[string]interface{}{}
	}
	var changes []change
	collectChanges(schema.LookupPath(cue.ParsePath("parameter")), nil, v.Parameter, 0, &changes)

	render1 := func(name string, param map[string]interface{}) Variant {
		in := map[string]interface{}{"parameter": param, "context": v.Context}
		if v.Workload != nil {
			in["workload"] = v.Workload
		}
		values, _ := yaml.Marshal(in)
		shown, _ := yaml.Marshal(param)
		r := render(ctx, Request{Path: req.Path, Source: req.Source, Values: values})
		return Variant{Name: name, Values: string(shown), Objects: r.Objects, Workload: r.Workload, Error: r.Error}
	}
	out := Exploration{Base: render1("the values file", v.Parameter), Variants: []Variant{}}
	if len(changes) > maxVariants {
		changes, out.Truncated = changes[:maxVariants], true
	}
	for _, c := range changes {
		out.Variants = append(out.Variants, render1(c.name, withChange(v.Parameter, c)))
	}
	return out, nil
}

// collectChanges adds the changes worth rendering of each parameter under v.
func collectChanges(v cue.Value, path []string, base map[string]interface{}, depth int, out *[]change) {
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return
	}
	for it.Next() {
		label := strings.TrimSuffix(it.Selector().String(), "?")
		if unq, err := strconv.Unquote(label); err == nil {
			label = unq
		}
		if strings.HasPrefix(label, "_") || strings.HasPrefix(label, "#") {
			continue
		}
		f := it.Value()
		p := append(append([]string{}, path...), label)
		name := strings.Join(p, ".")
		cur, set := valueAt(base, p)
		def, hasDefault := f.Default()
		if f.IncompleteKind() == cue.StructKind {
			// Inside an optional struct the values leave out, one field alone
			// would lack the fields required beside it.
			if depth < maxDepth && (set || !it.IsOptional()) {
				collectChanges(f, p, base, depth+1, out)
			}
			continue
		}
		if set && (it.IsOptional() || hasDefault) {
			what := "its default"
			if !hasDefault {
				what = "it is optional"
			}
			*out = append(*out, change{name: fmt.Sprintf("%s unset (%s)", name, what), path: p, unset: true})
		}
		current := cur
		if !set && hasDefault {
			_ = def.Decode(&current)
		}
		if !set && !hasDefault && it.IsOptional() {
			if sample, ok := sampleOf(f); ok {
				*out = append(*out, change{name: name + ": " + show(sample), path: p, value: sample})
			}
			continue
		}
		if op, args := f.Expr(); op == cue.OrOp {
			for _, a := range args {
				var x interface{}
				if !a.IsConcrete() || a.Decode(&x) != nil || equal(x, current) {
					continue
				}
				*out = append(*out, change{name: name + ": " + show(x), path: p, value: x})
			}
			continue
		}
		if f.IncompleteKind() == cue.BoolKind {
			if b, ok := current.(bool); ok {
				*out = append(*out, change{name: fmt.Sprintf("%s: %v", name, !b), path: p, value: !b})
			}
			continue
		}
		if f.IncompleteKind()&cue.NumberKind != 0 {
			lo, hi := bounds(f, f.IncompleteKind() == cue.IntKind)
			for _, b := range []struct {
				at   *float64
				what string
			}{{lo, "its minimum"}, {hi, "its maximum"}} {
				if b.at == nil {
					continue
				}
				var x interface{} = *b.at
				if f.IncompleteKind() == cue.IntKind {
					x = int64(*b.at)
				}
				if !equal(x, current) {
					*out = append(*out, change{name: fmt.Sprintf("%s: %s (%s)", name, show(x), b.what), path: p, value: x})
				}
			}
		}
	}
}

// bounds are the least and greatest values a number's constraints allow, as
// far as they say.
func bounds(v cue.Value, isInt bool) (lo, hi *float64) {
	var walk func(cue.Value)
	walk = func(x cue.Value) {
		op, args := x.Expr()
		switch op {
		case cue.AndOp:
			for _, a := range args {
				walk(a)
			}
		case cue.GreaterThanEqualOp, cue.GreaterThanOp:
			if n, err := args[0].Float64(); err == nil {
				if op == cue.GreaterThanOp && isInt {
					n++
				}
				if op == cue.GreaterThanEqualOp || isInt {
					lo = &n
				}
			}
		case cue.LessThanEqualOp, cue.LessThanOp:
			if n, err := args[0].Float64(); err == nil {
				if op == cue.LessThanOp && isInt {
					n--
				}
				if op == cue.LessThanEqualOp || isInt {
					hi = &n
				}
			}
		case cue.NoOp:
			if len(args) == 1 && !args[0].Equals(x) {
				walk(args[0])
			}
		}
	}
	walk(v)
	return lo, hi
}

// sampleOf is a value an optional parameter can be set to: a disjunction's
// first value, a number's least, or an example of its kind.
func sampleOf(v cue.Value) (interface{}, bool) {
	if op, args := v.Expr(); op == cue.OrOp {
		for _, a := range args {
			var x interface{}
			if a.IsConcrete() && a.Decode(&x) == nil {
				return x, true
			}
		}
	}
	switch k := v.IncompleteKind(); {
	case k == cue.StringKind:
		return "example", true
	case k == cue.BoolKind:
		return true, true
	case k&cue.NumberKind != 0:
		if lo, _ := bounds(v, k == cue.IntKind); lo != nil {
			if k == cue.IntKind {
				return int64(*lo), true
			}
			return *lo, true
		}
		return int64(1), true
	}
	return nil, false
}

// valueAt is the value at path of a parameter map, and whether it is set.
func valueAt(m map[string]interface{}, path []string) (interface{}, bool) {
	var cur interface{} = m
	for _, p := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		if cur, ok = mm[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// withChange is a copy of base with c made.
func withChange(base map[string]interface{}, c change) map[string]interface{} {
	b, _ := json.Marshal(base)
	out := map[string]interface{}{}
	_ = json.Unmarshal(b, &out)
	m := out
	for _, p := range c.path[:len(c.path)-1] {
		next, ok := m[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[p] = next
		}
		m = next
	}
	last := c.path[len(c.path)-1]
	if c.unset {
		delete(m, last)
	} else {
		m[last] = c.value
	}
	return out
}

// show is a value as CUE writes it.
func show(x interface{}) string {
	b, err := json.Marshal(x)
	if err != nil {
		return fmt.Sprint(x)
	}
	return string(b)
}

// equal is whether two decoded values are the same, a number however it was decoded.
func equal(a, b interface{}) bool {
	return show(a) == show(b)
}
