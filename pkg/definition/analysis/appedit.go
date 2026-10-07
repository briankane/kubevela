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
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// What can be added to an Application.
const (
	AddComponent    = "component"
	AddTrait        = "trait"
	AddPolicy       = "policy"
	AddWorkflowStep = "workflow-step"
)

// addDefType is the definition type of what is added.
var addDefType = map[string]string{
	AddComponent:    componentType,
	AddTrait:        traitType,
	AddPolicy:       policyType,
	AddWorkflowStep: workflowStepType,
}

// AppLens is a place in an Application to add something, at a 1-based line.
type AppLens struct {
	Line int
	Kind string
}

// AppEdit replaces Range of an Application with Snippet.
type AppEdit struct {
	Range   Range
	Snippet string
}

// appDoc is an Application document: its root mapping and spec.
type appDoc struct {
	root, spec *yaml.Node
	// first and last are its lines.
	first, last int
}

// applications are the Application documents of src, each with its lines.
func applications(src string) []appDoc {
	var out []appDoc
	dec := yaml.NewDecoder(strings.NewReader(src))
	var starts []int
	var docs []*yaml.Node
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if !errors.Is(err, io.EOF) {
				return out
			}
			break
		}
		if len(n.Content) == 0 {
			continue
		}
		docs = append(docs, n.Content[0])
		starts = append(starts, n.Content[0].Line)
	}
	lines := strings.Count(src, "\n") + 1
	for i, root := range docs {
		last := lines
		if i+1 < len(starts) {
			last = starts[i+1] - 1
		}
		if root.Kind != yaml.MappingNode || !applicationKind(scalar(mapValue(root, "apiVersion")), scalar(mapValue(root, "kind"))) {
			continue
		}
		spec := mapValue(root, "spec")
		if spec == nil || spec.Kind != yaml.MappingNode {
			continue
		}
		out = append(out, appDoc{root: root, spec: spec, first: root.Line, last: last})
	}
	return out
}

// mapKey and mapValue are a mapping's key and value nodes for key.
func mapKey(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// ApplicationLenses are where each Application in src can have something
// added: a policy and a workflow step at their lists, or at spec without
// them; a component at components; a trait at each component.
func ApplicationLenses(src string) []AppLens {
	var out []AppLens
	for _, app := range applications(src) {
		specLine := mapKey(app.root, "spec").Line
		at := func(key *yaml.Node) int {
			if key != nil {
				return key.Line
			}
			return specLine
		}
		out = append(out,
			AppLens{Line: at(mapKey(app.spec, "policies")), Kind: AddPolicy},
			AppLens{Line: at(mapKey(app.spec, "workflow")), Kind: AddWorkflowStep},
			AppLens{Line: at(mapKey(app.spec, "components")), Kind: AddComponent})
		if comps := mapValue(app.spec, "components"); comps != nil && comps.Kind == yaml.SequenceNode {
			for _, c := range comps.Content {
				out = append(out, AppLens{Line: c.Line, Kind: AddTrait})
			}
		}
	}
	return out
}

// AddToApplication is the edit adding an item of kind, of definition type
// typeName and named name where it takes one, to the Application at the
// 1-based line of src: a trait to the component the line is in. The item
// has a tab stop for each property its type requires. A missing list is
// written with it; the text around is kept as it is.
func AddToApplication(src string, line int, kind, typeName, name string, opts Options) (AppEdit, error) {
	defType, ok := addDefType[kind]
	if !ok {
		return AppEdit{}, fmt.Errorf("cannot add a %s to an Application", kind)
	}
	var app *appDoc
	for _, a := range applications(src) {
		if line >= a.first && line <= a.last {
			a := a
			app = &a
		}
	}
	if app == nil {
		return AppEdit{}, fmt.Errorf("no Application at line %d", line)
	}
	lines := strings.Split(src, "\n")
	item, err := appItemLines(kind, defType, typeName, name, opts)
	if err != nil {
		return AppEdit{}, err
	}
	switch kind {
	case AddTrait:
		comps := mapValue(app.spec, "components")
		var comp *yaml.Node
		if comps != nil && comps.Kind == yaml.SequenceNode {
			for i, c := range comps.Content {
				end := app.last
				if i+1 < len(comps.Content) {
					end = comps.Content[i+1].Line - 1
				}
				if line >= c.Line && line <= end {
					comp = c
				}
			}
		}
		if comp == nil {
			return AppEdit{}, fmt.Errorf("put the cursor in a component to add a trait to it")
		}
		return addToList(lines, comp, "traits", item), nil
	case AddComponent:
		return addToList(lines, app.spec, "components", item), nil
	case AddPolicy:
		return addToList(lines, app.spec, "policies", item), nil
	default:
		wf := mapValue(app.spec, "workflow")
		if wf == nil {
			return addToList(lines, app.spec, "workflow", append([]string{"steps:"}, indentLines(asList(item), 2)...)), nil
		}
		return addToList(lines, wf, "steps", item), nil
	}
}

// appItemLines is an item's YAML, as a list item's lines from its dash: its
// name where it takes one, its type, and its required properties.
func appItemLines(kind, defType, typeName, name string, opts Options) ([]string, error) {
	if opts.Applications == nil {
		return nil, fmt.Errorf("no %s types are known", kind)
	}
	def, ok := opts.Applications.Lookup(defType, typeName)
	if !ok {
		return nil, fmt.Errorf("no %s type %s in the workspace, the cluster or KubeVela's own", kind, typeName)
	}
	var out []string
	if kind != AddTrait {
		if name == "" {
			return nil, fmt.Errorf("a %s needs a name", kind)
		}
		out = append(out, "name: "+name)
	}
	out = append(out, "type: "+def.Name)
	if info, ok := infoOf(def, opts); ok && len(info.required) > 0 {
		out = append(out, "properties:")
		for i, r := range info.required {
			out = append(out, fmt.Sprintf("  %s: ${%d}", r, i+1))
		}
	}
	out[len(out)-1] += "$0"
	return out, nil
}

// asList makes an item's lines a list item: a dash before the first, the
// rest under it.
func asList(item []string) []string {
	out := make([]string, len(item))
	for i, l := range item {
		if i == 0 {
			out[i] = "- " + l
		} else {
			out[i] = "  " + l
		}
	}
	return out
}

func indentLines(ls []string, n int) []string {
	pad := strings.Repeat(" ", n)
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = pad + l
	}
	return out
}

// indentOf is how many spaces a line starts with.
func indentOf(l string) int {
	return len(l) - len(strings.TrimLeft(l, " "))
}

// blockEnd is the last line, 1-based, of the block starting at the 1-based
// line start whose own indentation is indent: the lines after it that are
// blank or indented deeper, trailing blank lines left out.
func blockEnd(lines []string, start, indent int) int {
	end := start
	for i := start; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if indentOf(l) <= indent {
			break
		}
		end = i + 1
	}
	return end
}

// insertAfter is the edit inserting text after the 1-based line.
func insertAfter(lines []string, line int, text []string) AppEdit {
	body := strings.Join(text, "\n")
	if line >= len(lines) || line == len(lines)-1 && lines[len(lines)-1] == "" {
		// At the end of the file: after its last line, on lines of their own.
		last := line
		if last > len(lines) {
			last = len(lines)
		}
		pos := Position{Line: last, Column: len(lines[last-1]) + 1}
		prefix := "\n"
		if lines[last-1] == "" {
			prefix = ""
		}
		suffix := ""
		if lines[last-1] == "" {
			suffix = "\n"
		}
		return AppEdit{Range: Range{Start: pos, End: pos}, Snippet: prefix + body + suffix}
	}
	pos := Position{Line: line + 1, Column: 1}
	return AppEdit{Range: Range{Start: pos, End: pos}, Snippet: body + "\n"}
}

// addToList is the edit adding item to the list at key under the mapping
// parent: after its last item; replacing [] or nothing written for it; or,
// with no key, the key and the list after the parent's last line.
func addToList(lines []string, parent *yaml.Node, key string, item []string) AppEdit {
	keyNode, list := mapKey(parent, key), mapValue(parent, key)
	childIndent := parent.Column - 1
	if len(parent.Content) > 0 {
		childIndent = parent.Content[0].Column - 1
	}
	if keyNode == nil {
		// The parent ends where its last value does.
		last := parent.Content[len(parent.Content)-1]
		end := blockEnd(lines, last.Line, childIndent)
		text := append([]string{key + ":"}, indentLines(asList(item), 2)...)
		if key == "workflow" {
			text = append([]string{key + ":"}, indentLines(item, 2)...)
		}
		return insertAfter(lines, end, indentLines(text, childIndent))
	}
	keyIndent := keyNode.Column - 1
	if list != nil && list.Kind == yaml.SequenceNode && len(list.Content) > 0 && list.Style&yaml.FlowStyle == 0 {
		first := list.Content[0]
		dash := strings.LastIndex(lines[first.Line-1][:first.Column-1], "-")
		lastItem := list.Content[len(list.Content)-1]
		end := blockEnd(lines, lastItem.Line, dash)
		return insertAfter(lines, end, indentLines(asList(item), dash))
	}
	// [] or nothing: the list goes under its key, in place of what is there.
	l := lines[keyNode.Line-1]
	colon := keyNode.Column - 1 + len(key) + strings.Index(l[keyNode.Column-1+len(key):], ":") + 1
	start := Position{Line: keyNode.Line, Column: colon + 1}
	end := Position{Line: keyNode.Line, Column: len(l) + 1}
	text := asList(item)
	if key == "workflow" {
		text = item
	}
	body := "\n" + strings.Join(indentLines(text, keyIndent+2), "\n")
	return AppEdit{Range: Range{Start: start, End: end}, Snippet: body}
}
