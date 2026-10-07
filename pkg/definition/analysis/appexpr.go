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
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"

	"github.com/oam-dev/kubevela/pkg/definition/celexpr"
	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
	"github.com/oam-dev/kubevela/pkg/sources"
)

// appExprItem is an item of an Application whose properties may hold
// expressions: where it is, and the surface it is read on.
type appExprItem struct {
	path    string
	surface string
	// index is a source's place in spec.sources, which it may read only
	// before; -1 for anything else.
	index int
	props cue.Value
}

// checkExpressions checks the $(...) expressions in an Application's
// properties as KubeVela's admission does: each parses and compiles, reads
// only the roots its place may, a source declared in spec.sources (a
// source, only one declared before it) and an attribute its definition's
// schema declares, and a component the Application has.
func (d *document) checkExpressions(app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	declared := map[string]int{}
	sourceTypes := map[string]string{}
	components := map[string]bool{}
	var items []appExprItem
	list := func(path string, each func(i int, v cue.Value)) {
		it, err := app.LookupPath(cue.ParsePath(path)).List()
		if err != nil {
			return
		}
		for i := 0; it.Next(); i++ {
			each(i, it.Value())
		}
	}
	str := func(v cue.Value, path string) string {
		s, _ := v.LookupPath(cue.ParsePath(path)).String()
		return s
	}
	list("spec.sources", func(i int, v cue.Value) {
		name := str(v, "name")
		declared[name] = i
		sourceTypes[name] = str(v, "type")
		items = append(items, appExprItem{path: fmt.Sprintf("spec.sources.%d", i), surface: sources.SurfaceSource, index: i, props: v.LookupPath(cue.ParsePath("properties"))})
	})
	list("spec.components", func(i int, v cue.Value) {
		components[str(v, "name")] = true
		at := fmt.Sprintf("spec.components.%d", i)
		items = append(items, appExprItem{path: at, surface: sources.SurfaceComponent, index: -1, props: v.LookupPath(cue.ParsePath("properties"))})
		it, err := v.LookupPath(cue.ParsePath("traits")).List()
		if err != nil {
			return
		}
		for j := 0; it.Next(); j++ {
			items = append(items, appExprItem{path: fmt.Sprintf("%s.traits.%d", at, j), surface: sources.SurfaceTrait, index: -1, props: it.Value().LookupPath(cue.ParsePath("properties"))})
		}
	})
	list("spec.policies", func(i int, v cue.Value) {
		items = append(items, appExprItem{path: fmt.Sprintf("spec.policies.%d", i), surface: d.policySurface(str(v, "type")), index: -1, props: v.LookupPath(cue.ParsePath("properties"))})
	})
	list("spec.workflow.steps", func(i int, v cue.Value) {
		at := fmt.Sprintf("spec.workflow.steps.%d", i)
		items = append(items, appExprItem{path: at, surface: sources.SurfaceWorkflowStep, index: -1, props: v.LookupPath(cue.ParsePath("properties"))})
		it, err := v.LookupPath(cue.ParsePath("subSteps")).List()
		if err != nil {
			return
		}
		for j := 0; it.Next(); j++ {
			items = append(items, appExprItem{path: fmt.Sprintf("%s.subSteps.%d", at, j), surface: sources.SurfaceWorkflowStep, index: -1, props: it.Value().LookupPath(cue.ParsePath("properties"))})
		}
	})

	schemas := map[string]cue.Value{}
	schemaOf := func(binding string) (cue.Value, bool) {
		if s, ok := schemas[binding]; ok {
			return s, s.Exists()
		}
		var schema cue.Value
		if def, ok := d.opts.Applications.Lookup(sourceType, sourceTypes[binding]); ok {
			if sd, ok := definitionHeaderOf(def); ok {
				if v, ok := sd.evaluate(); ok {
					schema = v.LookupPath(cue.ParsePath(templateLabel + ".schema"))
				}
			}
		}
		schemas[binding] = schema
		return schema, schema.Exists()
	}

	var diags []Diagnostic
	for _, item := range items {
		if !item.props.Exists() {
			continue
		}
		var props interface{}
		if err := item.props.Decode(&props); err != nil {
			continue
		}
		roots := sources.RootsFor(item.surface)
		walkStrings(props, item.path+".properties", func(path, raw string) {
			if !propexpr.MayContainExpr(raw) {
				return
			}
			r, ok := nearestFieldRange(fields, path)
			if !ok {
				return
			}
			report := func(msg string) {
				diags = append(diags, Diagnostic{Range: r, Severity: SeverityError, Message: msg})
			}
			parsed, err := propexpr.Parse(raw)
			if err != nil {
				report(err.Error())
				return
			}
			for _, frag := range parsed.Fragments {
				if frag.Expr == "" {
					continue
				}
				refs, err := celexpr.PropertyReferences(frag.Expr)
				if err != nil {
					report(fmt.Sprintf("$(%s): %v", frag.Expr, err))
					continue
				}
				for _, ref := range refs {
					if !contains(roots, ref.Root) {
						report(fmt.Sprintf("%q cannot be read here; this surface permits %q", ref.Root, roots))
						continue
					}
					switch {
					case ref.IsSource() && len(ref.Path) > 0:
						binding := ref.Path[0]
						at, ok := declared[binding]
						switch {
						case !ok:
							report(fmt.Sprintf("source %q is not declared in spec.sources", binding))
						case item.index >= 0 && at >= item.index:
							report(fmt.Sprintf("source at index %d can only depend on prior sources, but %q is at index %d", item.index, binding, at))
						default:
							if schema, ok := schemaOf(binding); ok && len(ref.Path) > 1 && !schemaHasPath(schema, ref.Path[1:]) {
								report(fmt.Sprintf("path %q is not declared in schema of SourceDefinition %q", strings.Join(ref.Path[1:], "."), sourceTypes[binding]))
							}
						}
					case ref.IsComponent() && len(ref.Path) > 0 && !components[ref.Path[0]]:
						report(fmt.Sprintf("component %q is not a component of this Application", ref.Path[0]))
					}
				}
			}
		})
	}
	return diags
}

// policySurface is the surface a policy of a type reads on: a built-in
// policy's and an Application-scoped one's read context alone; one KubeVela
// renders from CUE may read sources too.
func (d *document) policySurface(typeName string) string {
	if builtinPolicies[typeName] {
		return sources.SurfacePolicy
	}
	if def, ok := d.opts.Applications.Lookup(policyType, typeName); ok {
		if kindFromText(def.CUE, policyType) == applicationPolicy {
			return sources.SurfacePolicyApp
		}
	}
	return sources.SurfacePolicyRendered
}

// schemaHasPath reports whether a source's schema declares path, as
// admission looks it up: a field, optional or not, or a pattern's value; a
// numeric segment a list's element; anything below a field of any type, and
// a segment holding a dot, accepted.
func schemaHasPath(schema cue.Value, path []string) bool {
	v := schema
	for _, seg := range path {
		if v.IncompleteKind() == cue.TopKind || strings.Contains(seg, ".") {
			return true
		}
		if _, err := strconv.Atoi(seg); err == nil && v.IncompleteKind()&cue.ListKind != 0 {
			v = v.LookupPath(cue.MakePath(cue.AnyIndex))
			continue
		}
		next, ok := schemaField(v, seg)
		if !ok {
			return false
		}
		v = next
	}
	return true
}

// schemaField is a struct's field, optional or not, or the value of a
// pattern it declares, [string]: T. Any struct allows any field, so a
// pattern counts only where one gives a type: what an open struct with none
// gives is top.
func schemaField(v cue.Value, name string) (cue.Value, bool) {
	// A field is found by listing them: looking up an optional one that is
	// not declared gives top, as if it were.
	if it, err := v.Fields(cue.Optional(true)); err == nil {
		for it.Next() {
			if it.Selector().Unquoted() == name {
				return it.Value(), true
			}
		}
	}
	if f := v.LookupPath(cue.MakePath(cue.AnyString)); f.Exists() && f.IncompleteKind() != cue.TopKind {
		return f, true
	}
	return cue.Value{}, false
}

// walkStrings calls fn with each string in a decoded YAML tree, and its path:
// keys and list indexes joined by dots, as an Application's fields are.
func walkStrings(node interface{}, path string, fn func(path, raw string)) {
	switch x := node.(type) {
	case string:
		fn(path, x)
	case map[string]interface{}:
		for k, v := range x {
			walkStrings(v, path+"."+k, fn)
		}
	case []interface{}:
		for i, v := range x {
			walkStrings(v, fmt.Sprintf("%s.%d", path, i), fn)
		}
	}
}

// nearestFieldRange is the range of the field at path, or of the nearest
// field above it: a list's element has no field of its own.
func nearestFieldRange(fields map[string]*ast.Field, path string) (Range, bool) {
	for p := path; p != ""; {
		if r, ok := fieldRange(fields, p); ok {
			return r, true
		}
		i := strings.LastIndex(p, ".")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return Range{}, false
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
