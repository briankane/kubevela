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

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
)

// Completion is one candidate to complete the text before the cursor with.
type Completion struct {
	// Label is what the candidate is shown as.
	Label string
	// Insert replaces the last Replace bytes before the cursor.
	Insert  string
	Replace int
	Doc     string
	// Detail is the candidate's type, when it has one.
	Detail string
}

var (
	markerNameTyped  = regexp.MustCompile(`^\s*//\s*\+((?:[A-Za-z][A-Za-z0-9]*(?::[A-Za-z0-9]*)?)?)$`)
	markerValueTyped = regexp.MustCompile(`^\s*//\s*\+([A-Za-z][A-Za-z0-9]*(?::[A-Za-z][A-Za-z0-9]*)?)=(\S*)$`)
)

// CompleteMarker completes a marker in a comment, given its line up to the
// cursor: a marker's name after `// +`, or one of the values it takes after
// `=`.
func CompleteMarker(before string) []Completion {
	if m := markerNameTyped.FindStringSubmatch(before); m != nil {
		typed := m[1]
		var out []Completion
		for _, mk := range Markers() {
			if !strings.HasPrefix(strings.ToLower(mk.Name), strings.ToLower(typed)) {
				continue
			}
			insert := mk.Name
			if !mk.Bare {
				insert += "="
			}
			out = append(out, Completion{Label: "+" + mk.Name, Insert: insert, Replace: len(typed), Doc: mk.Doc})
		}
		return out
	}
	if m := markerValueTyped.FindStringSubmatch(before); m != nil {
		mk := findMarker(Markers(), m[1])
		if mk == nil {
			return nil
		}
		values := append([]string{}, mk.Values...)
		sort.Strings(values)
		var out []Completion
		for _, v := range values {
			if strings.HasPrefix(v, m[2]) {
				out = append(out, Completion{Label: v, Insert: v, Replace: len(m[2]), Doc: mk.Doc})
			}
		}
		return out
	}
	return nil
}

var (
	contextTyped = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.$#])context((?:\.[A-Za-z_][A-Za-z0-9_]*)*)\.([A-Za-z0-9_]*)$`)
	headerType   = regexp.MustCompile(`(?m)^\s+type:\s*"([^"]+)"`)
)

// CompleteContext completes a read of context, given the whole document,
// whose header names the definition type, and its line up to the cursor: the
// fields that type's template can read after `context.`, or a struct field's
// fields after `context.a.`.
func CompleteContext(doc, before string) []Completion {
	m := contextTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	t := headerType.FindStringSubmatch(doc)
	if t == nil {
		return nil
	}
	fields := ContextFields(t[1])
	if fields == nil {
		return nil
	}
	path, typed := strings.Split(strings.TrimPrefix(m[1], "."), "."), m[2]
	if m[1] == "" {
		path = nil
	}
	var out []Completion
	if len(path) == 0 {
		for _, f := range fields {
			if strings.HasPrefix(f.Name, typed) {
				out = append(out, Completion{Label: f.Name, Insert: f.Name, Replace: len(typed), Doc: f.Doc, Detail: f.Type})
			}
		}
		return out
	}
	var root *ContextField
	for i := range fields {
		if fields[i].Name == path[0] {
			root = &fields[i]
		}
	}
	if root == nil {
		return nil
	}
	v := cuecontext.New().CompileString("x: " + root.Type).LookupPath(cue.ParsePath("x"))
	for _, p := range path[1:] {
		v = v.LookupPath(cue.MakePath(cue.Str(p)))
	}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	for it.Next() {
		name := it.Selector().Unquoted()
		if strings.HasPrefix(name, typed) {
			out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: typeOf(it.Value())})
		}
	}
	return out
}

func typeOf(v cue.Value) string {
	b, err := format.Node(v.Syntax())
	if err != nil {
		return ""
	}
	return string(b)
}
