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

package lsp

import (
	"strings"

	"cuelang.org/go/cue"
)

// fallbackSchema is a parameter's JSON schema read field by field, for one
// CUE's OpenAPI generator refuses, as it does a bounded number in a
// disjunction (*1 | int & >=1): each field's type, default, choices, help,
// and whether it must be given, which is when it is neither optional nor
// defaulted.
func fallbackSchema(v cue.Value) map[string]interface{} {
	out := map[string]interface{}{}
	if help := usage(v); help != "" {
		out["description"] = help
	}
	if d, ok := v.Default(); ok && d.IsConcrete() && d.IncompleteKind()&(cue.StructKind|cue.ListKind) == 0 {
		var x interface{}
		if d.Decode(&x) == nil {
			out["default"] = x
		}
	}
	if op, args := v.Expr(); op == cue.OrOp {
		var choices []interface{}
		for _, a := range args {
			var x interface{}
			if !a.IsConcrete() || a.IncompleteKind()&(cue.StructKind|cue.ListKind) != 0 || a.Decode(&x) != nil {
				choices = nil
				break
			}
			choices = append(choices, x)
		}
		if len(choices) > 1 {
			out["enum"] = choices
		}
	}
	switch k := v.IncompleteKind(); {
	case k == cue.StringKind:
		out["type"] = "string"
	case k == cue.IntKind:
		out["type"] = "integer"
	case k == cue.FloatKind || k == cue.NumberKind:
		out["type"] = "number"
	case k == cue.BoolKind:
		out["type"] = "boolean"
	case k == cue.ListKind:
		out["type"] = "array"
		out["items"] = fallbackSchema(v.LookupPath(cue.MakePath(cue.AnyIndex)))
	case k == cue.StructKind:
		out["type"] = "object"
		props := map[string]interface{}{}
		var required []string
		if it, err := v.Fields(cue.Optional(true)); err == nil {
			for it.Next() {
				name := it.Selector().Unquoted()
				props[name] = fallbackSchema(it.Value())
				if _, defaulted := it.Value().Default(); !it.IsOptional() && !defaulted {
					required = append(required, name)
				}
			}
		}
		if len(props) > 0 {
			out["properties"] = props
		} else if pattern := v.LookupPath(cue.MakePath(cue.AnyString)); pattern.Exists() {
			out["additionalProperties"] = fallbackSchema(pattern)
		}
		if len(required) > 0 {
			out["required"] = required
		}
	}
	return out
}

// usage is a field's help, from its comment's +usage= where it has one.
func usage(v cue.Value) string {
	var lines []string
	for _, g := range v.Doc() {
		lines = append(lines, strings.TrimSpace(g.Text()))
	}
	text := strings.Join(lines, "\n")
	if _, after, found := strings.Cut(text, "+usage="); found {
		text = after
	}
	if before, _, found := strings.Cut(text, "+short="); found {
		text = before
	}
	return strings.TrimSpace(text)
}
