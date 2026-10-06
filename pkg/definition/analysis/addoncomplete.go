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
	"cuelang.org/go/cue/parser"
)

// addonTopLevel are the fields each kind of addon CUE file sets for
// KubeVela to read, with what each is.
var addonTopLevel = map[string][]Completion{
	addonKindTemplate: {
		{Label: "output", Doc: "The Application the addon installs."},
		{Label: "outputs", Doc: "Kubernetes objects installed beside the Application, by name."},
	},
	addonKindResource: {
		{Label: "output", Doc: "The component this file renders, added to the addon's Application. Not read from a file of package main."},
	},
	addonKindConfig: {
		{Label: "metadata", Doc: "The config template's name, alias, description, scope and sensitivity."},
		{Label: "template", Doc: "The config's parameter, and the Secret (output) or objects (outputs) it renders."},
	},
	addonKindView: {
		{Label: "parameter", Doc: "What a query passes the view."},
		{Label: "status", Doc: "What a query returns, unless it names another field."},
		{Label: "export", Doc: "The name of the field a query returns, in place of status."},
	},
	addonKindNotes: {
		{Label: "notes", Doc: "The message printed once the addon is enabled."},
	},
	addonKindParameter: {
		{Label: "parameter", Doc: "The addon's parameters: what `vela addon enable` takes, and VelaUX's form for them."},
	},
}

var keyTyped = regexp.MustCompile(`^\s*([A-Za-z_$#][A-Za-z0-9_$#-]*)?$`)

// CompleteAddonFile completes in an addon's CUE file, at cursor in doc, as
// KubeVela compiles that file: a field of a value read (parameter, context,
// output, or a helper), a field the schema allows where a key is typed, a
// member of a package the file may import, or the import itself. It is false
// when path is not an addon's CUE file.
func CompleteAddonFile(path, doc string, cursor int, opts Options) ([]Completion, bool) {
	_, kind, ok := addonCUEFile(path)
	if !ok {
		return nil, false
	}
	before := doc[:cursor]
	line := before[strings.LastIndex(before, "\n")+1:]
	d := &document{path: path, opts: opts}
	set := d.addonPackages(kind)
	if typed, ok := importPathTyped(before); ok {
		return completeImportFrom(set, typed), true
	}
	if m := memberTyped.FindStringSubmatch(line); m != nil {
		if importPath := importedAs(doc, m[1]); importPath != "" {
			return completeMember(set, importPath, m[2]), true
		}
	}
	if m := valueTyped.FindStringSubmatchIndex(before); m != nil {
		root, typed := before[m[2]:m[3]], before[m[6]:m[7]]
		var chain []string
		if c := strings.TrimPrefix(before[m[4]:m[5]], "."); c != "" {
			chain = strings.Split(c, ".")
		}
		// The text at the cursor does not parse; a placeholder stands in for it.
		v, ok := d.addonValue(rootOf(doc, m[2], cursor), kind, lookupRoot(root))
		if !ok {
			return nil, true
		}
		return fieldsOf(walk(v, chain), typed), true
	}
	if m := keyTyped.FindStringSubmatch(line); m != nil {
		typed := m[1]
		frames := openFrames(before[:len(before)-len(typed)])
		if len(frames) == 0 {
			var out []Completion
			for _, c := range addonTopLevel[kind] {
				if strings.HasPrefix(c.Label, typed) {
					c.Insert, c.Replace = c.Label, len(typed)
					out = append(out, c)
				}
			}
			return out, true
		}
		var labels []string
		for _, f := range frames {
			labels = append(labels, labelsOf(f.prefix)...)
		}
		if len(labels) == 0 {
			return nil, true
		}
		patched := doc[:cursor-len(typed)] + doc[wordEnd(doc, cursor):]
		v, ok := d.addonValue(patched, kind, lookupRoot(labels[0]))
		if !ok {
			return nil, true
		}
		return fieldsOf(walk(v, labels[1:]), typed), true
	}
	return nil, true
}

// rootOf is doc with the reference being typed, from start to the end of
// the word at cursor, replaced by a placeholder, so it parses.
func rootOf(doc string, start, cursor int) string {
	return doc[:start] + "_" + doc[wordEnd(doc, cursor):]
}

// wordEnd is the end of the word cursor is in, or cursor.
func wordEnd(doc string, cursor int) int {
	for cursor < len(doc) && isWordByteAt(doc[cursor]) {
		cursor++
	}
	return cursor
}

// lookupRoot is where a reference's root is looked up: a parameter is read
// from its closed copy, which holds what the enable may add.
func lookupRoot(name string) string {
	if name == parameterLabel {
		return "#velaAddonParameter"
	}
	return name
}

// addonPackages are the packages a kind of addon file may import.
func (d *document) addonPackages(kind string) packages {
	switch kind {
	case addonKindConfig:
		return packages{builtin: configPackages}
	case addonKindView:
		return packages{builtin: workflowPackages, ext: d.opts.Externals}
	}
	return packages{builtin: stdlibOnly}
}

// addonValue is the value at root of doc, compiled as the kind of addon file
// d is.
func (d *document) addonValue(doc, kind, root string) (cue.Value, bool) {
	addonRoot, _, _ := addonCUEFile(d.path)
	f, err := parser.ParseFile(d.path, doc, parser.ParseComments)
	if err != nil {
		return cue.Value{}, false
	}
	d.src, d.file = []byte(doc), f
	c, _, ok := d.addonCompile(addonRoot, kind)
	if !ok {
		return cue.Value{}, false
	}
	v, _ := d.compileAddon(c)
	if !v.Exists() {
		return cue.Value{}, false
	}
	if root == "output" && c.output != "" {
		root = c.output
	}
	if strings.HasPrefix(root, "#") {
		if p := v.LookupPath(cue.MakePath(cue.Def(root))); p.Exists() {
			return p, true
		}
		root = parameterLabel
	}
	return schemaChild(v, cue.Str(root)), true
}

// walk follows chain from v, through optional and pattern fields.
func walk(v cue.Value, chain []string) cue.Value {
	for _, step := range chain {
		if !v.Exists() {
			return v
		}
		v = schemaChild(v, cue.Str(step))
	}
	return v
}

// fieldsOf are the fields of v named from typed on, with their types and docs.
func fieldsOf(v cue.Value, typed string) []Completion {
	if !v.Exists() {
		return nil
	}
	if v.IncompleteKind() == cue.ListKind {
		// Every element has the list's schema, so any one stands for the
		// one being written.
		if it, err := v.List(); err == nil && it.Next() {
			v = it.Value()
		} else {
			v = v.LookupPath(cue.MakePath(cue.AnyIndex))
		}
	}
	it, err := v.Fields(cue.Optional(true))
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

// importedAs is the import path doc imports as alias, or "".
func importedAs(doc, alias string) string {
	for _, spec := range importSpec.FindAllStringSubmatch(doc, -1) {
		name := spec[1]
		if name == "" {
			name = spec[2][strings.LastIndex(spec[2], "/")+1:]
		}
		if name == alias {
			return spec[2]
		}
	}
	return ""
}

// HoverAddonFile describes the word at offset in an addon's CUE file: the
// completion entry for it where it is read.
func HoverAddonFile(path, doc string, offset int, opts Options) (string, bool) {
	if offset < 0 || offset >= len(doc) || !isWordByteAt(doc[offset]) {
		return "", false
	}
	start, end := offset, offset
	for start > 0 && isWordByteAt(doc[start-1]) {
		start--
	}
	for end < len(doc) && isWordByteAt(doc[end]) {
		end++
	}
	name := doc[start:end]
	got, ok := CompleteAddonFile(path, doc[:end]+doc[end:], end, opts)
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

// AddonFileKind is the addon and kind of an addon's CUE file at path, for a
// client to choose addon completion and hover.
func AddonFileKind(path string) (root, kind string, ok bool) {
	return addonCUEFile(path)
}
