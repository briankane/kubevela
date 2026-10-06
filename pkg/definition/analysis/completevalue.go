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
	"sort"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

var valueTyped = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.$#])([A-Za-z_$#][A-Za-z0-9_$#]*)((?:\.[A-Za-z_$#][A-Za-z0-9_$#]*)*)\.([A-Za-z0-9_$#]*)$`)

// bundledKinds are Kubernetes' own kinds, for what reading a resource returns.
var bundledKinds = sync.OnceValue(func() *kubeschema.Schemas {
	s, err := kubeschema.Builtin()
	if err != nil {
		return kubeschema.New()
	}
	return s
})

// CompleteValueAt completes a field of a value the template declares, at
// cursor in doc: `_req.` offers what _req has, `_req.$returns.` what the
// function it calls returns. A function that reads a resource returns that
// resource, so its result is offered from the kind's schema when the call
// names apiVersion and kind.
func CompleteValueAt(doc string, cursor int, ext *Externals) []Completion {
	before := doc[:cursor]
	m := valueTyped.FindStringSubmatchIndex(before)
	if m == nil {
		return nil
	}
	root := before[m[2]:m[3]]
	chain := strings.Split(strings.TrimPrefix(before[m[4]:m[5]], "."), ".")
	if before[m[4]:m[5]] == "" {
		chain = nil
	}
	typed := before[m[6]:m[7]]
	if root == "context" || root == parameterLabel {
		return nil
	}
	for _, spec := range importSpec.FindAllStringSubmatch(doc, -1) {
		alias := spec[1]
		if alias == "" {
			alias = spec[2][strings.LastIndex(spec[2], "/")+1:]
		}
		if alias == root {
			return nil
		}
	}
	// The text at the cursor does not parse; a placeholder stands in for it.
	patched := doc[:m[2]] + "_" + doc[cursor:]
	f, err := parser.ParseFile("complete.cue", patched, parser.ParseComments)
	if err != nil {
		return nil
	}
	d, ok := newDocument("complete.cue", []byte(patched), f)
	if !ok {
		return nil
	}
	d.opts = Options{Externals: ext}
	declared := map[string][]*ast.Field{}
	allTopLevelFields(d.template.Value, declared)
	if len(declared[root]) == 0 {
		return nil
	}
	v, ok := d.evaluate()
	if !ok {
		return nil
	}
	cur := v.LookupPath(cue.MakePath(cue.Str(templateLabel), rootSelector(root)))
	for _, step := range chain {
		parent := cur
		cur = schemaChild(cur, cue.Str(step))
		if step == "$returns" {
			cur = withResourceKind(parent, cur)
		}
		if !cur.Exists() {
			return nil
		}
	}
	it, err := cur.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		name := it.Selector().Unquoted()
		if strings.HasPrefix(name, typed) {
			out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: kindName(it.Value()), Doc: usageOf(it.Value())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func rootSelector(name string) cue.Selector {
	switch {
	case strings.HasPrefix(name, "#"):
		return cue.Def(name)
	case strings.HasPrefix(name, "_"):
		return cue.Hid(name, "_")
	}
	return cue.Str(name)
}

// withResourceKind is what a call returns, given the kind of the resource its
// $params name, when that kind's schema is known.
func withResourceKind(call, returns cue.Value) cue.Value {
	res := call.LookupPath(cue.ParsePath("$params.resource"))
	apiVersion, err1 := res.LookupPath(cue.ParsePath("apiVersion")).String()
	kind, err2 := res.LookupPath(cue.ParsePath("kind")).String()
	if err1 != nil || err2 != nil {
		return returns
	}
	gvk := kubeschema.ParseGVK(apiVersion, kind)
	kinds := bundledKinds()
	src, ok := kinds.CUE(gvk)
	if !ok {
		return returns
	}
	schema := returns.Context().CompileString(src).LookupPath(cue.ParsePath(kubeschema.Root(gvk)))
	if !schema.Exists() {
		return returns
	}
	return schema
}

// kindName is a value's kind, briefly.
func kindName(v cue.Value) string {
	switch v.IncompleteKind() {
	case cue.StructKind:
		return "struct"
	case cue.ListKind:
		return "list"
	}
	return typeName(v)
}

// evaluate compiles the template as checkTemplate does, for its values.
func (d *document) evaluate() (cue.Value, bool) {
	bi := build.NewContext().NewInstance(d.path, nil)
	bi.Imports = d.packages().imports()
	if err := bi.AddSyntax(d.compileFile()); err != nil {
		return cue.Value{}, false
	}
	return cuecontext.New().BuildInstance(bi), true
}
