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
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// kindCheck is one object of the template checked against its kind: the path
// of the object under template, and the kind it declares.
type kindCheck struct {
	path []string
	gvk  kubeschema.GVK
}

// checkKinds checks output and each of outputs against the schema of the
// kind it declares, when its apiVersion and kind are concrete and the schema
// is known. f is the compiled template, v its value.
func (d *document) checkKinds(f *ast.File, v cue.Value) []Diagnostic {
	kinds := d.opts.Kinds
	if kinds == nil {
		return nil
	}
	var checks []kindCheck
	add := func(path ...string) {
		obj := v.LookupPath(cue.MakePath(selectors(append([]string{templateLabel}, path...))...))
		apiVersion, err1 := obj.LookupPath(cue.ParsePath("apiVersion")).String()
		kind, err2 := obj.LookupPath(cue.ParsePath("kind")).String()
		if err1 != nil || err2 != nil {
			return
		}
		if gvk := kubeschema.ParseGVK(apiVersion, kind); kinds.Has(gvk) {
			checks = append(checks, kindCheck{path: path, gvk: gvk})
		}
	}
	add("output")
	if it, err := v.LookupPath(cue.ParsePath(templateLabel + ".outputs")).Fields(); err == nil {
		for it.Next() {
			add("outputs", it.Selector().Unquoted())
		}
	}
	if len(checks) == 0 {
		return nil
	}

	gvks := make([]kubeschema.GVK, 0, len(checks))
	for _, c := range checks {
		gvks = append(gvks, c.gvk)
	}
	src, _ := kinds.CUE(gvks...)
	schema, err := parser.ParseFile("kubeschema", src)
	if err != nil {
		return nil
	}
	decls := append(append([]ast.Decl{}, f.Decls...), schema.Decls...)
	named := map[string]kubeschema.GVK{}
	for _, c := range checks {
		quoted := make([]string, 0, len(c.path)+1)
		for _, p := range append([]string{templateLabel}, c.path...) {
			quoted = append(quoted, strconv.Quote(p))
		}
		decls = append(decls, mustField(strings.Join(quoted, ": ")+": "+kubeschema.Root(c.gvk)))
		named[strings.Join(c.path, ".")] = c.gvk
	}
	bi := build.NewContext().NewInstance(d.path, nil)
	bi.Imports = d.packages().imports()
	if err := bi.AddSyntax(&ast.File{Filename: d.path, Decls: decls}); err != nil {
		return nil
	}
	checked := cuecontext.New().BuildInstance(bi)
	err = checked.Err()
	if err == nil {
		err = checked.Validate()
	}
	var diags []Diagnostic
	for _, e := range cueerrors.Errors(err) {
		for _, diag := range d.fromErrors(e, templateLabel) {
			if gvk, ok := kindAt(named, e.Path()); ok {
				diag.Message += fmt.Sprintf(" (%s)", describe(gvk))
			}
			diags = append(diags, diag)
		}
	}
	return diags
}

// kindAt is the kind of the checked object an error's path is under.
func kindAt(named map[string]kubeschema.GVK, path []string) (kubeschema.GVK, bool) {
	path = trimLabel(path, templateLabel)
	for n := len(path); n > 0; n-- {
		if gvk, ok := named[strings.Join(path[:n], ".")]; ok {
			return gvk, true
		}
	}
	return kubeschema.GVK{}, false
}

func describe(gvk kubeschema.GVK) string {
	if gvk.Group == "" {
		return gvk.Version + " " + gvk.Kind
	}
	return gvk.Group + "/" + gvk.Version + " " + gvk.Kind
}

func selectors(path []string) []cue.Selector {
	sels := make([]cue.Selector, 0, len(path))
	for _, p := range path {
		sels = append(sels, cue.Str(p))
	}
	return sels
}

// checkPatchKinds checks a trait's patch, as a partial object, against the
// kinds its appliesToWorkloads names as resources ("deployments.apps") whose
// schemas are known, and reports what fits none of them. A patch that is a JSON patch, a list of operations, is
// not an object, so is not checked.
func (d *document) checkPatchKinds(f *ast.File, v cue.Value) []Diagnostic {
	kinds := d.opts.Kinds
	if kinds == nil || d.templateKind() != traitType {
		return nil
	}
	patch := v.LookupPath(cue.MakePath(cue.Str(templateLabel), cue.Str("patch")))
	if !patch.Exists() || patch.IncompleteKind() != cue.StructKind {
		return nil
	}
	var gvks []kubeschema.GVK
	for _, r := range d.appliesToWorkloads() {
		if gvk := kinds.KindOf(r); gvk != (kubeschema.GVK{}) && kinds.Has(gvk) {
			gvks = append(gvks, gvk)
		}
	}
	// A trait may patch each kind it applies to differently, choosing by a
	// parameter, so only a field wrong for every kind is reported. Each kind
	// is checked in a build of its own: CUE reports an error once per
	// position, however many checks share it.
	var order []string
	first := map[string]Diagnostic{}
	kindsOf := map[string][]string{}
	for _, gvk := range gvks {
		for _, diag := range d.checkPatchKind(f, kinds, gvk) {
			key := fmt.Sprintf("%d:%d:%s", diag.Range.Start.Line, diag.Range.Start.Column, diag.Message)
			if _, seen := first[key]; !seen {
				order = append(order, key)
				first[key] = diag
			}
			kindsOf[key] = append(kindsOf[key], describe(gvk))
		}
	}
	var diags []Diagnostic
	for _, key := range order {
		if len(kindsOf[key]) < len(gvks) {
			continue
		}
		diag := first[key]
		diag.Message += " (" + strings.Join(kindsOf[key], ", ") + ")"
		diags = append(diags, diag)
	}
	return diags
}

// checkPatchKind checks a trait's patch against one kind.
func (d *document) checkPatchKind(f *ast.File, kinds *kubeschema.Schemas, gvk kubeschema.GVK) []Diagnostic {
	src, ok := kinds.CUE(gvk)
	if !ok {
		return nil
	}
	schema, err := parser.ParseFile("kubeschema", src)
	if err != nil {
		return nil
	}
	const check = "velaPatchCheck"
	decls := append(append([]ast.Decl{}, f.Decls...), schema.Decls...)
	decls = append(decls, mustField(check+": "+templateLabel+".patch & "+kubeschema.Root(gvk)))
	bi := build.NewContext().NewInstance(d.path, nil)
	bi.Imports = d.packages().imports()
	if err := bi.AddSyntax(&ast.File{Filename: d.path, Decls: decls}); err != nil {
		return nil
	}
	checked := cuecontext.New().BuildInstance(bi)
	err = checked.Err()
	if err == nil {
		err = checked.Validate()
	}
	var diags []Diagnostic
	for _, e := range cueerrors.Errors(err) {
		if path := e.Path(); len(path) == 0 || path[0] != check {
			continue
		}
		for _, diag := range d.fromErrors(e, check) {
			diag.Message = "patch." + diag.Message
			diags = append(diags, diag)
		}
	}
	return diags
}

// appliesToWorkloads are the workloads the trait's header says it applies to.
func (d *document) appliesToWorkloads() []string {
	var out []string
	for _, h := range d.headers {
		attrs, ok := fieldIn(h, "attributes")
		if !ok {
			continue
		}
		applies, ok := fieldIn(attrs, "appliesToWorkloads")
		if !ok {
			continue
		}
		list, ok := applies.Value.(*ast.ListLit)
		if !ok {
			continue
		}
		for _, e := range list.Elts {
			if lit, ok := e.(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
