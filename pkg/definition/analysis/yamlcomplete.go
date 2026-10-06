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
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// listItem stands for a list's element in a YAML key path.
const listItem = "[]"

var (
	yamlKeyTyped   = regexp.MustCompile(`^(\s*)(- )?([A-Za-z0-9_$#.-]*)$`)
	yamlValueTyped = regexp.MustCompile(`^(\s*)(- )?([A-Za-z0-9_$#.-]+):\s*(\S*)$`)
	yamlKeyLine    = regexp.MustCompile(`^(\s*)(- )?([A-Za-z0-9_$#."'-]+):(\s*(.*))$`)
	yamlTypeLine   = regexp.MustCompile(`(?m)^(apiVersion|kind):\s*["']?([^"'\s#]+)`)
)

// CompleteYAMLFile completes a key, or a value the schema names, in YAML
// KubeVela reads: an addon's metadata.yaml, template.yaml or UI schemas, a
// Package resource, or a Kubernetes object of a kind whose schema is known.
// It is false for YAML of none of these.
func CompleteYAMLFile(path, doc string, cursor int, opts Options) ([]Completion, bool) {
	before := doc[:cursor]
	start := 0
	if i := strings.LastIndex(before, "\n---"); i >= 0 {
		start = i + 1
		if nl := strings.IndexByte(before[start:], '\n'); nl >= 0 {
			start += nl + 1
		} else {
			start = len(before)
		}
	}
	end := len(doc)
	if i := strings.Index(doc[cursor:], "\n---"); i >= 0 {
		end = cursor + i
	}
	schema, ok := yamlSchema(path, doc[start:end], opts)
	if !ok {
		return nil, false
	}
	lines := strings.Split(before[start:], "\n")
	last, above := lines[len(lines)-1], lines[:len(lines)-1]
	if m := yamlValueTyped.FindStringSubmatch(last); m != nil {
		keyIndent := len(m[1])
		if m[2] != "" {
			keyIndent += 2
		}
		chain := yamlPath(above, keyIndent, m[2] != "")
		return valuesOf(walkYAML(schema, append(chain, m[3])), m[4]), true
	}
	if m := yamlKeyTyped.FindStringSubmatch(last); m != nil {
		keyIndent := len(m[1])
		if m[2] != "" {
			keyIndent += 2
		}
		return fieldsOf(walkYAML(schema, yamlPath(above, keyIndent, m[2] != "")), m[3]), true
	}
	return nil, true
}

// yamlPath is the path of keys, with listItem for a list's element, to the
// mapping a key at indent belongs to, read from the lines above it. item is
// set when the key starts a list item.
func yamlPath(above []string, indent int, item bool) []string {
	var rev []string
	level, listParent := indent, false
	if item {
		rev, level, listParent = append(rev, listItem), indent-2, true
	}
	for i := len(above) - 1; i >= 0 && (level > 0 || listParent); i-- {
		m := yamlKeyLine.FindStringSubmatch(above[i])
		if m == nil {
			continue
		}
		dash := len(m[1])
		keyIndent := dash
		if m[2] != "" {
			keyIndent += 2
		}
		empty := strings.TrimSpace(m[5]) == "" || strings.HasPrefix(strings.TrimSpace(m[5]), "#")
		switch {
		case keyIndent == level && m[2] != "" && !listParent:
			// A sibling that starts the item the key is in.
			rev, level, listParent = append(rev, listItem), dash, true
		case listParent && m[2] == "" && keyIndent <= level && empty:
			rev, level, listParent = append(rev, strings.Trim(m[3], `"'`)), keyIndent, false
		case !listParent && keyIndent < level && empty:
			rev = append(rev, strings.Trim(m[3], `"'`))
			level = keyIndent
			if m[2] != "" {
				rev, level, listParent = append(rev, listItem), dash, true
			}
		}
	}
	out := make([]string, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// walkYAML follows a YAML key path from v. The schemas recurse only into
// themselves, as a UI parameter's subParameters are UI parameters, so where
// CUE reports the recursion as a cycle, the struct that held the field
// stands for its value.
func walkYAML(v cue.Value, chain []string) cue.Value {
	owner := v
	for _, step := range chain {
		if !v.Exists() {
			return v
		}
		if step == listItem {
			v = v.LookupPath(cue.MakePath(cue.AnyIndex))
		} else {
			owner = v
			v = schemaChild(v, cue.Str(step))
		}
		if v.Err() != nil {
			v = owner
		}
	}
	return v
}

// valuesOf are the values v allows, when it names them, from typed on.
func valuesOf(v cue.Value, typed string) []Completion {
	if !v.Exists() {
		return nil
	}
	var values []string
	if v.IncompleteKind() == cue.BoolKind {
		values = []string{"true", "false"}
	} else if op, args := v.Expr(); op == cue.OrOp {
		for _, a := range args {
			if s, err := a.String(); err == nil {
				values = append(values, s)
			}
		}
	}
	var out []Completion
	for _, val := range values {
		if strings.HasPrefix(val, typed) {
			out = append(out, Completion{Label: val, Insert: val, Replace: len(typed)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// yamlSchema is the schema of YAML at path whose current document is doc.
func yamlSchema(path, doc string, opts Options) (cue.Value, bool) {
	ctx := cuecontext.New()
	compile := func(src, def string) (cue.Value, bool) {
		v := ctx.CompileString(src).LookupPath(cue.ParsePath(def))
		return v, v.Exists()
	}
	if root, ok := AddonRoot(path); ok {
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch {
		case rel == addonMetadataFile:
			return compile(addonMetaCUE, "#velaAddonMeta")
		case rel == addonTemplateYAML:
			return compile(addonApplicationCUE, "#addonApplication")
		case len(parts) == 2 && (parts[0] == addonUISchemas || parts[0] == "uischemas"):
			return compile(uiSchemaCUE, "#UISchema")
		}
	}
	found := map[string]string{}
	for _, m := range yamlTypeLine.FindAllStringSubmatch(doc, -1) {
		if _, seen := found[m[1]]; !seen {
			found[m[1]] = m[2]
		}
	}
	apiVersion, kind := found["apiVersion"], found["kind"]
	if apiVersion == "" || kind == "" {
		return cue.Value{}, false
	}
	if kind == "Package" && strings.HasPrefix(apiVersion, "cue.oam.dev/") {
		return compile(packageCUE, "#Package")
	}
	kinds := opts.Kinds
	if kinds == nil {
		kinds = bundledKinds()
	}
	gvk := kubeschema.ParseGVK(apiVersion, kind)
	src, ok := kinds.CUE(gvk)
	if !ok {
		return cue.Value{}, false
	}
	return compile(src, kubeschema.Root(gvk))
}

// HoverYAMLFile describes the key at offset in YAML KubeVela reads: its type
// and what it is.
func HoverYAMLFile(path, doc string, offset int, opts Options) (string, bool) {
	if offset < 0 || offset >= len(doc) || !isWordByteAt(doc[offset]) {
		return "", false
	}
	end := wordEnd(doc, offset)
	if end >= len(doc) || doc[end] != ':' {
		return "", false
	}
	start := offset
	for start > 0 && isWordByteAt(doc[start-1]) {
		start--
	}
	name := doc[start:end]
	got, ok := CompleteYAMLFile(path, doc[:end], end, opts)
	if !ok {
		return "", false
	}
	for _, c := range got {
		if c.Label == name {
			return hoverText(name, c.Detail, c.Doc), true
		}
	}
	return "", false
}
