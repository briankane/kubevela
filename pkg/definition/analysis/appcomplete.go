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
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

var (
	appKindLine    = regexp.MustCompile(`(?m)^kind:\s*Application\s*$`)
	appVersionLine = regexp.MustCompile(`(?m)^apiVersion:\s*core\.oam\.dev/`)
	itemTypeLine   = regexp.MustCompile(`^(\s*)(?:-\s+)?type:\s*["']?([A-Za-z0-9][A-Za-z0-9.-]*)`)
	itemPropsLine  = regexp.MustCompile(`^(\s*)(?:-\s+)?properties:`)
)

// isApplicationText reports whether a YAML document is a KubeVela Application.
func isApplicationText(doc string) bool {
	return appKindLine.MatchString(doc) && appVersionLine.MatchString(doc)
}

// itemDefinitionType is the type of definition the items of the list at
// path name: path ends with the list's listItem.
func itemDefinitionType(path []string) string {
	if len(path) < 2 || path[len(path)-1] != listItem {
		return ""
	}
	list := strings.Join(path[:len(path)-1], ".")
	switch {
	case list == "spec.components":
		return componentType
	case list == "spec.policies":
		return policyType
	case strings.HasPrefix(list, "spec.components.") && strings.HasSuffix(list, ".traits"):
		return traitType
	case list == "spec.workflow.steps", strings.HasPrefix(list, "spec.workflow.steps.") && strings.HasSuffix(list, ".subSteps"):
		return workflowStepType
	}
	return ""
}

// itemLines are the lines of the list item starting at line start of doc,
// and the indent of its keys.
func itemLines(doc []string, start int) ([]string, int) {
	if start < 0 || start >= len(doc) {
		return nil, 0
	}
	dash := len(doc[start]) - len(strings.TrimLeft(doc[start], " "))
	out := []string{doc[start]}
	for _, l := range doc[start+1:] {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && indent <= dash {
			break
		}
		out = append(out, l)
	}
	return out, dash + 2
}

// itemType is the type a list item names, and whether it writes properties.
func itemType(lines []string, keyIndent int) (string, bool) {
	name, props := "", false
	for _, l := range lines {
		if m := itemTypeLine.FindStringSubmatch(l); m != nil && keyLevel(l, m[1]) == keyIndent {
			name = m[2]
		}
		if m := itemPropsLine.FindStringSubmatch(l); m != nil && keyLevel(l, m[1]) == keyIndent {
			props = true
		}
	}
	return name, props
}

// keyLevel is the column a line's key starts at, past any list dash.
func keyLevel(line, indent string) int {
	rest := strings.TrimLeft(line, " ")
	if strings.HasPrefix(rest, "- ") {
		return len(indent) + 2
	}
	return len(indent)
}

// paramInfo is what completion shows of a definition's parameter.
type paramInfo struct {
	required []string
	summary  string
}

var paramInfos sync.Map

// infoOf is what completion shows of a definition's parameter, worked out
// once for each definition's text.
func infoOf(def AppDefinition, opts Options) (paramInfo, bool) {
	key := fmt.Sprintf("%s|%s|%x", def.Source, def.Name, sha256.Sum256([]byte(def.CUE)))
	if v, ok := paramInfos.Load(key); ok {
		return v.(paramInfo), true
	}
	ctx := cuecontext.New()
	param, ok := def.parameterIn(ctx, opts)
	if !ok {
		return paramInfo{}, false
	}
	info := paramInfo{required: requiredMissing(param, ctx.CompileString("{}"))}
	required := map[string]bool{}
	for _, r := range info.required {
		required[r] = true
	}
	var b strings.Builder
	b.WriteString("**Parameters**\n")
	it, err := param.Fields(cue.Optional(true))
	for n := 0; err == nil && it.Next() && n < 25; n++ {
		name := it.Selector().Unquoted()
		fmt.Fprintf(&b, "\n- `%s` %s", name, kindName(it.Value()))
		if required[name] {
			b.WriteString(", required")
		}
		if def, ok := it.Value().Default(); ok && def.IsConcrete() {
			if js, err := def.MarshalJSON(); err == nil && len(js) <= 40 {
				fmt.Fprintf(&b, ", default `%s`", js)
			}
		}
		if u := usageOf(it.Value()); u != "" {
			b.WriteString(": " + u)
		}
	}
	info.summary = b.String()
	paramInfos.Store(key, info)
	return info, true
}

// completeApplication completes in an Application's YAML: the definitions
// a type may name, and a definition's parameters under properties. It is
// false where the Application's own schema completes instead.
func completeApplication(doc, above []string, last string, opts Options) ([]Completion, bool) {
	if m := yamlValueTyped.FindStringSubmatch(last); m != nil {
		keyIndent := len(m[1])
		if m[2] != "" {
			keyIndent += 2
		}
		path, lines := yamlPathItems(above, keyIndent, m[2] != "")
		if m[3] == "type" {
			defType := itemDefinitionType(path)
			if defType == "" {
				return nil, false
			}
			item, _ := itemLines(doc, lines[len(lines)-1])
			_, hasProps := itemType(item, keyIndent)
			return typeCompletions(defType, m[4], keyIndent, hasProps, opts), true
		}
		param, ok := propertiesAt(doc, append(path, m[3]), lines, opts)
		if !ok {
			return nil, false
		}
		return valuesOf(param, m[4]), true
	}
	if m := yamlKeyTyped.FindStringSubmatch(last); m != nil {
		keyIndent := len(m[1])
		if m[2] != "" {
			keyIndent += 2
		}
		path, lines := yamlPathItems(above, keyIndent, m[2] != "")
		param, ok := propertiesAt(doc, path, lines, opts)
		if !ok {
			return nil, false
		}
		return fieldsOf(param, m[3]), true
	}
	return nil, false
}

// propertiesAt is the schema at path, under an item's properties, from the
// parameter of the definition the item's type names.
func propertiesAt(doc, path []string, lines []int, opts Options) (cue.Value, bool) {
	for k := len(path) - 1; k > 0; k-- {
		if path[k] != "properties" || path[k-1] != listItem {
			continue
		}
		defType := itemDefinitionType(path[:k])
		item, keyIndent := itemLines(doc, lines[k-1])
		name, _ := itemType(item, keyIndent)
		def, ok := opts.Applications.Lookup(defType, name)
		if defType == "" || !ok {
			return cue.Value{}, false
		}
		param, ok := def.parameterIn(cuecontext.New(), opts)
		if !ok {
			return cue.Value{}, false
		}
		return walkYAML(param, path[k+1:]), true
	}
	return cue.Value{}, false
}

// typeCompletions are the definitions of defType a type may name, each with
// its parameters and, where the item has no properties yet, a snippet
// writing those it requires.
func typeCompletions(defType, typed string, keyIndent int, hasProps bool, opts Options) []Completion {
	var out []Completion
	indent := strings.Repeat(" ", keyIndent)
	for _, def := range opts.Applications.List(defType) {
		if !strings.HasPrefix(def.Name, typed) {
			continue
		}
		c := Completion{Label: def.Name, Insert: def.Name, Replace: len(typed), Detail: def.Source, Doc: def.Description}
		if info, ok := infoOf(def, opts); ok {
			c.Doc = strings.TrimSpace(def.Description + "\n\n" + info.summary)
			if !hasProps && len(info.required) > 0 {
				var b strings.Builder
				b.WriteString(def.Name + "\n" + indent + "properties:")
				for i, r := range info.required {
					fmt.Fprintf(&b, "\n%s  %s: ${%d}", indent, r, i+1)
				}
				c.Snippet = b.String()
			}
		}
		out = append(out, c)
	}
	return out
}
