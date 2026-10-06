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
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/oam-dev/kubevela/pkg/definition"
	velaast "github.com/oam-dev/kubevela/pkg/definition/ast"
)

// statusPaths are the header fields whose native CUE velaast.EncodeMetadata
// checks; its errors start with the path they are about.
var statusPaths = []string{
	"attributes.status.healthPolicy",
	"attributes.status.customStatus",
	"attributes.status.details",
}

// checkHeader checks the definition header the way `vela def apply` does.
func (d *document) checkHeader() []Diagnostic {
	h := d.headers[0]
	var diags []Diagnostic
	for _, extra := range d.headers[1:] {
		diags = append(diags, d.at(extra.Label.Pos(),
			fmt.Sprintf("a definition file declares one definition; %q is a second", labelName(extra.Label))))
	}
	t, ok := fieldIn(h, "type")
	switch {
	case !ok:
		diags = append(diags, d.at(h.Label.Pos(),
			fmt.Sprintf("missing type: one of %s", strings.Join(definition.ValidDefinitionTypes(), ", "))))
	case definition.DefinitionTypeToKind[d.typ] == "":
		diags = append(diags, d.at(t.Value.Pos(),
			fmt.Sprintf("unknown type %q: one of %s", d.typ, strings.Join(definition.ValidDefinitionTypes(), ", "))))
	}
	if len(diags) > 0 {
		return diags
	}
	return d.checkMetadata()
}

// checkMetadata runs the status checks and builds the Definition from the
// header. Both rewrite the header, so they work on a fresh parse of it.
func (d *document) checkMetadata() []Diagnostic {
	f, err := parser.ParseFile(d.path, d.src, parser.ParseComments)
	if err != nil {
		return nil
	}
	fresh, ok := newDocument(d.path, d.src, f)
	if !ok {
		return nil
	}
	h := fresh.headers[0]
	if err := velaast.EncodeMetadata(h); err != nil {
		return []Diagnostic{d.at(statusPos(h, err.Error()), err.Error())}
	}
	v := cuecontext.New().BuildFile(&ast.File{Filename: d.path, Decls: []ast.Decl{h}})
	if err := v.Err(); err != nil {
		return d.fromErrors(err, "")
	}
	def := definition.Definition{}
	if err := def.FromCUE(&v, templateLabel); err != nil {
		return []Diagnostic{d.at(h.Label.Pos(), err.Error())}
	}
	return nil
}

// statusPos is the status field an EncodeMetadata error is about.
func statusPos(h *ast.Field, msg string) token.Pos {
	for _, p := range statusPaths {
		if !strings.HasPrefix(msg, p) {
			continue
		}
		if f, ok := velaast.GetFieldByPath(h, p); ok {
			return f.Pos()
		}
	}
	return h.Label.Pos()
}
