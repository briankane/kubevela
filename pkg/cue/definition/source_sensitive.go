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

package definition

import (
	"strings"

	"cuelang.org/go/cue/ast"
	cueparser "cuelang.org/go/cue/parser"
)

// Sensitive-path extraction, moved here from pkg/appfile so the engine can
// derive what a template marks rather than being told. appfile imports this
// package, so it could not live there and be reachable from the resolver.

// ExtractSensitiveOutputPaths reports the schema paths a source template marks
// +sensitive, so a caller need not compute them and cannot get them wrong.
//
// Derived rather than supplied, for the same reason schemas are: both are
// projections of the template, and accepting either separately lets the two
// disagree. A caller that supplied templates but forgot the paths would get
// silent under-redaction.
func ExtractSensitiveOutputPaths(template string) []string {
	f, err := cueparser.ParseFile("-", template, cueparser.ParseComments)
	if err != nil || f == nil {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, block := range sensitiveMarkerBlocks {
		st := findTopLevelStruct(f, block)
		if st == nil {
			continue
		}
		var found []string
		collectSensitivePaths(st, nil, &found)
		for _, path := range found {
			if seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// findTopLevelStruct returns the named top-level struct of a template, or nil.

func collectSensitivePaths(st *ast.StructLit, prefix []string, out *[]string) {
	for _, elt := range st.Elts {
		field, ok := elt.(*ast.Field)
		if !ok {
			continue
		}
		name := labelName(field.Label)
		if name == "" {
			continue
		}
		path := append(prefix, name)
		if hasSensitiveMarker(field) {
			*out = append(*out, strings.Join(path, "."))
		}
		if nested, ok := field.Value.(*ast.StructLit); ok {
			collectSensitivePaths(nested, path, out)
		}
	}
}

func hasSensitiveMarker(field *ast.Field) bool {
	for _, cg := range field.Comments() {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "+sensitive") {
				return true
			}
		}
	}
	return false
}

func labelName(label ast.Label) string {
	switch v := label.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.BasicLit:
		return strings.Trim(v.Value, "\"")
	default:
		return ""
	}
}

// WorkflowClient cache retrieved workflow if ApplicationRevision not exists in appfile
// else use the workflow in ApplicationRevision

// sensitiveMarkerBlocks are the template blocks a `// +sensitive` marker is
// honoured in. schema: is where KEP-2.16 documents the marker and where its
// examples place it; output: is where the first implementation read it from.
// Both are scanned so a definition written either way still redacts, rather than
// silently exposing the value because the marker sat in the other block.
var sensitiveMarkerBlocks = []string{"schema", "output"}

func findTopLevelStruct(f *ast.File, name string) *ast.StructLit {
	for _, decl := range f.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		if labelName(field.Label) != name {
			continue
		}
		if st, ok := field.Value.(*ast.StructLit); ok {
			return st
		}
	}
	return nil
}

// RedactedFields is what this resolution consumed, with anything the definition
// marks +sensitive blanked.
//
// A method rather than a note in the docs: ConsumedFields holds real values
// because the render needs them, and a caller reporting them directly would
// publish a credential. Making the safe form the easy one is the only version of
// this that survives a caller who has not read the comment.
//
// extra adds paths beyond the definition's own, for a caller that masks more.
func (s SourceResolutionStatus) RedactedFields(extra ...string) map[string]interface{} {
	masks := map[string]struct{}{}
	for _, p := range append(append([]string{}, s.SensitivePaths...), extra...) {
		if p != "" {
			masks[p] = struct{}{}
		}
	}
	out := make(map[string]interface{}, len(s.ConsumedFields))
	for path, v := range s.ConsumedFields {
		out[path] = RedactValue(path, v, masks)
	}
	return out
}

// maskedPath reports whether a consumed field is covered by a mask, either
// exactly or by sitting underneath one.
//
// The descent matters. A marker can only be written where the schema declares a
// field, so a source exposing an open struct - `properties: _`, whose shape is
// whatever template produced it - has nowhere to put a marker except on the
// struct itself. Matching exactly would mask a read of `properties` and publish
// `properties.token` beside it, which is the one case the marker exists for.
// redactValue blanks anything marked sensitive inside a read value.
//
// maskedPath alone is not enough. It answers "is this path at or below a mark",
// which covers reading db.password directly, but an expression may substitute a
// whole collection - "$(source.creds.db)" - and then the read path is db while
// the mark is db.password, one level below. Nothing matched and the secret went
// into status verbatim.
//
// Marks are schema paths and carry no list indices, since collectSensitivePaths
// descends only into struct literals. So elements of a list share their parent's
// path: a mark of "members.token" applies to the token of every member.
func RedactValue(path string, v interface{}, masks map[string]struct{}) interface{} {
	if len(masks) == 0 {
		return v
	}
	if MaskedPath(path, masks) {
		return "***"
	}
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, child := range val {
			out[k] = RedactValue(joinMaskPath(path, k), child, masks)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(val))
		for _, child := range val {
			out = append(out, RedactValue(path, child, masks))
		}
		return out
	default:
		return v
	}
}

func joinMaskPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func MaskedPath(path string, masks map[string]struct{}) bool {
	if _, ok := masks[path]; ok {
		return true
	}
	for mask := range masks {
		if strings.HasPrefix(path, mask+".") {
			return true
		}
	}
	return false
}
