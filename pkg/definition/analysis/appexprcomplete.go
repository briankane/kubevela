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

	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
	"github.com/oam-dev/kubevela/pkg/sources"
)

var (
	// exprTyped is the expression being written after the last $( on a line
	// before the cursor: a root, and the selections after it.
	exprTyped = regexp.MustCompile(`\$\(([A-Za-z0-9_.\-]*)$`)
	// itemNameLine matches a list item's name line.
	itemNameLine = regexp.MustCompile(`^(\s*)(?:-\s+)?name:\s*"?([^"\s#]+)"?`)
)

// surfaceContexts are the context each surface's expressions read.
var surfaceContexts = map[string]propexpr.ContextSchema{
	sources.SurfaceComponent:      propexpr.ComponentContext,
	sources.SurfaceTrait:          propexpr.TraitContext,
	sources.SurfaceWorkflowStep:   propexpr.WorkflowStepContext,
	sources.SurfacePolicy:         propexpr.PolicyContext,
	sources.SurfacePolicyRendered: propexpr.RenderedPolicyContext,
	sources.SurfacePolicyApp:      propexpr.ScopedPolicyContext,
	// A source's own properties are read where its consumers read it.
	sources.SurfaceSource: propexpr.ComponentContext,
}

// appListItem is an item of one of an Application's lists, read from its
// text: the lines it spans, and its name and type.
type appListItem struct {
	start, end int
	name, typ  string
}

// appListItems are the items of the list under spec at key, read from the
// text, as it may not parse while an expression is written.
func appListItems(lines []string, key string) []appListItem {
	var items []appListItem
	in, keyIndent := false, 0
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentOf(l)
		if trimmed == key+":" {
			in, keyIndent = true, indent
			continue
		}
		if !in {
			continue
		}
		if indent <= keyIndent && !strings.HasPrefix(trimmed, "-") || indent < keyIndent {
			in = false
			continue
		}
		if strings.HasPrefix(trimmed, "-") && indent <= keyIndent+2 {
			if len(items) > 0 {
				items[len(items)-1].end = i - 1
			}
			items = append(items, appListItem{start: i, end: len(lines) - 1})
		}
		if len(items) == 0 {
			continue
		}
		cur := &items[len(items)-1]
		if m := itemNameLine.FindStringSubmatch(l); m != nil && cur.name == "" && indent <= keyIndent+4 {
			cur.name = m[2]
		}
		if m := itemTypeLine.FindStringSubmatch(l); m != nil && cur.typ == "" && indent <= keyIndent+4 {
			cur.typ = m[2]
		}
	}
	return items
}

// completeAppExpression completes inside a $( ) on the line at line of an
// Application's lines: the roots its place may read, the source bindings,
// a source's attributes from its definition's schema, the context fields,
// and the Application's other components.
func completeAppExpression(lines []string, line int, before string, opts Options) ([]Completion, bool) {
	m := exprTyped.FindStringSubmatch(before)
	if m == nil {
		return nil, false
	}
	path := yamlPath(lines[:line], indentOf(lines[line]), false)
	if len(path) < 2 || path[0] != "spec" {
		return nil, false
	}
	srcs := appListItems(lines, "sources")
	comps := appListItems(lines, "components")
	surface, self := exprPlace(lines, line, path, comps, opts)
	if surface == "" {
		return nil, false
	}
	parts := strings.Split(m[1], ".")
	typed := parts[len(parts)-1]
	var names []string
	detail := ""
	switch {
	case len(parts) == 1:
		names, detail = sources.RootsFor(surface), "root"
	case parts[0] == propexpr.SourceIdent && len(parts) == 2:
		for _, s := range srcs {
			// A source reads only those before the one it is.
			if surface == sources.SurfaceSource && line >= s.start && line <= s.end {
				break
			}
			names = append(names, s.name)
		}
		detail = "source"
	case parts[0] == propexpr.SourceIdent:
		names, detail = sourceAttributes(srcs, parts[1], parts[2:len(parts)-1], opts)
	case parts[0] == propexpr.ContextIdent && len(parts) == 2:
		names, detail = surfaceContexts[surface].ReadableFields(), "context"
	case parts[0] == propexpr.ComponentIdent && len(parts) == 2 && sources.SurfaceReadsComponents(surface):
		for _, c := range comps {
			if c.name != self {
				names = append(names, c.name)
			}
		}
		detail = "component"
	case parts[0] == propexpr.ComponentIdent && len(parts) == 3:
		names, detail = []string{"output", "outputs"}, "component's rendered objects"
	}
	var out []Completion
	for _, n := range names {
		if strings.HasPrefix(n, typed) {
			out = append(out, Completion{Label: n, Insert: n, Replace: len(typed), Detail: detail})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, true
}

// exprPlace is the surface an expression on line reads on, by its path in
// the Application, and for a component the component's own name.
func exprPlace(lines []string, line int, path []string, comps []appListItem, opts Options) (surface, self string) {
	switch {
	case path[1] == "sources":
		return sources.SurfaceSource, ""
	case path[1] == "components" && containsString(path, "traits"):
		return sources.SurfaceTrait, ""
	case path[1] == "components":
		for _, c := range comps {
			if line >= c.start && line <= c.end {
				self = c.name
			}
		}
		return sources.SurfaceComponent, self
	case path[1] == "policies":
		d := &document{opts: opts}
		for _, p := range appListItems(lines, "policies") {
			if line >= p.start && line <= p.end {
				return d.policySurface(p.typ), ""
			}
		}
	case path[1] == "workflow":
		return sources.SurfaceWorkflowStep, ""
	}
	return "", ""
}

// sourceAttributes are the attributes at path in the schema of the source
// definition the binding named is of, and what to call them.
func sourceAttributes(srcs []appListItem, binding string, path []string, opts Options) ([]string, string) {
	var typ string
	for _, s := range srcs {
		if s.name == binding {
			typ = s.typ
		}
	}
	def, ok := opts.Applications.Lookup(sourceType, typ)
	if !ok {
		return nil, ""
	}
	sd, ok := definitionHeaderOf(def)
	if !ok {
		return nil, ""
	}
	v, ok := sd.evaluate()
	if !ok {
		return nil, ""
	}
	schema := v.LookupPath(cue.ParsePath(templateLabel + ".schema"))
	for _, seg := range path {
		if schema, ok = schemaField(schema, seg); !ok {
			return nil, ""
		}
	}
	var names []string
	if it, err := schema.Fields(cue.Optional(true)); err == nil {
		for it.Next() {
			names = append(names, it.Selector().Unquoted())
		}
	}
	return names, typ + " schema"
}

func containsString(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}
