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

	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/definition/celexpr"
	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
	"github.com/oam-dev/kubevela/pkg/sources"
	webhookapp "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/application"
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
	// kind and typ are the definition its properties feed, typed against.
	kind, typ string
}

// checkExpressions checks the $(...) expressions in an Application's
// properties as KubeVela's admission does: each parses and compiles, reads
// only the roots its place may, a source declared in spec.sources (a
// source, only one declared before it) and an attribute its definition's
// schema declares, and a component the Application has.
func (d *document) checkExpressions(app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	items, declared, sourceTypes, components := d.expressionItems(app)
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

	schemaTexts := map[string]string{}
	for binding, typ := range sourceTypes {
		if def, ok := d.opts.Applications.Lookup(sourceType, typ); ok {
			if tmpl, ok := TemplateSource(def.Name+".cue", []byte(def.CUE)); ok {
				if text, err := webhookapp.SourceSchemaText(tmpl.Body); err == nil && text != "" {
					schemaTexts[binding] = text
				}
			}
		}
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
		params, desc := d.targetParameter(item)
		walkStringSegs(props, item.path+".properties", nil, func(path string, segs []string, raw string) {
			if !propexpr.MayContainExpr(raw) {
				return
			}
			r, ok := nearestFieldRange(fields, path)
			if !ok {
				return
			}
			reported := false
			report := func(msg string) {
				reported = true
				// An expression's fault is its own, beside any of the property's.
				diags = append(diags, Diagnostic{Range: r, Severity: SeverityError, Message: msg, distinct: true})
			}
			// What a read faults is said once: typing it would say it again.
			defer func() {
				if reported || desc == "" {
					return
				}
				if msg := webhookapp.ExpressionTargetError(raw, schemaTexts, surfaceContext(item), params, segs, desc); msg != "" {
					report(msg)
				}
			}()
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
						report(fmt.Sprintf("%q cannot be read here; this surface permits %q", ref.Root, strings.Join(roots, `", "`)))
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

// expressionItems are an Application's items whose properties may hold
// expressions, with the index of each source binding, its type, and the
// components' names.
func (d *document) expressionItems(app cue.Value) ([]appExprItem, map[string]int, map[string]string, map[string]bool) {
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
		items = append(items, appExprItem{path: at, surface: sources.SurfaceComponent, index: -1, props: v.LookupPath(cue.ParsePath("properties")), kind: componentType, typ: str(v, "type")})
		it, err := v.LookupPath(cue.ParsePath("traits")).List()
		if err != nil {
			return
		}
		for j := 0; it.Next(); j++ {
			items = append(items, appExprItem{path: fmt.Sprintf("%s.traits.%d", at, j), surface: sources.SurfaceTrait, index: -1, props: it.Value().LookupPath(cue.ParsePath("properties")), kind: traitType, typ: str(it.Value(), "type")})
		}
	})
	list("spec.policies", func(i int, v cue.Value) {
		items = append(items, appExprItem{path: fmt.Sprintf("spec.policies.%d", i), surface: d.policySurface(str(v, "type")), index: -1, props: v.LookupPath(cue.ParsePath("properties")), kind: policyType, typ: str(v, "type")})
	})
	list("spec.workflow.steps", func(i int, v cue.Value) {
		at := fmt.Sprintf("spec.workflow.steps.%d", i)
		items = append(items, appExprItem{path: at, surface: sources.SurfaceWorkflowStep, index: -1, props: v.LookupPath(cue.ParsePath("properties")), kind: workflowStepType, typ: str(v, "type")})
		it, err := v.LookupPath(cue.ParsePath("subSteps")).List()
		if err != nil {
			return
		}
		for j := 0; it.Next(); j++ {
			items = append(items, appExprItem{path: fmt.Sprintf("%s.subSteps.%d", at, j), surface: sources.SurfaceWorkflowStep, index: -1, props: it.Value().LookupPath(cue.ParsePath("properties")), kind: workflowStepType, typ: str(it.Value(), "type")})
		}
	})
	return items, declared, sourceTypes, components
}

// targetParameter is the parameter of the definition an item's properties
// feed, read as admission reads it, and how admission names it; "" where it
// is not typed, as a source's properties are not here.
func (d *document) targetParameter(item appExprItem) (cue.Value, string) {
	if item.kind == "" {
		return cue.Value{}, ""
	}
	desc := fmt.Sprintf("%s %q parameter", item.kind, item.typ)
	if item.kind == workflowStepType {
		desc = fmt.Sprintf("workflow step %q parameter", item.typ)
	}
	def, ok := d.opts.Applications.Lookup(item.kind, item.typ)
	if !ok {
		return cue.Value{}, desc
	}
	tmpl, ok := TemplateSource(def.Name+".cue", []byte(def.CUE))
	if !ok {
		return cue.Value{}, desc
	}
	params, _ := webhookapp.TargetParameter(tmpl.Body)
	return params, desc
}

// surfaceContext is the context an item's expressions are typed against,
// as admission chooses it.
func surfaceContext(item appExprItem) propexpr.ContextSchema {
	if item.kind == policyType {
		return appfile.PolicyContextSchema(item.typ, item.surface == sources.SurfacePolicyApp)
	}
	return surfaceContexts[item.surface]
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
	walkStringSegs(node, path, nil, func(path string, _ []string, raw string) { fn(path, raw) })
}

// walkStringSegs is walkStrings with each string's segments below path too,
// as admission names a property: a key may hold a dot.
func walkStringSegs(node interface{}, path string, segs []string, fn func(path string, segs []string, raw string)) {
	switch x := node.(type) {
	case string:
		fn(path, segs, x)
	case map[string]interface{}:
		for k, v := range x {
			walkStringSegs(v, path+"."+k, append(append([]string{}, segs...), k), fn)
		}
	case []interface{}:
		for i, v := range x {
			walkStringSegs(v, fmt.Sprintf("%s.%d", path, i), append(append([]string{}, segs...), strconv.Itoa(i)), fn)
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
