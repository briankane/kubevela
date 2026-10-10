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

package crdgen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"cuelang.org/go/cue/format"
	"gopkg.in/yaml.v3"
)

// Member is a CRD of a set, with the fields found to name the object it belongs to.
type Member struct {
	Info
	Refs []Ref `json:"refs,omitempty"`
}

// Ref is a field of a CRD's spec that names another object: a struct with a
// name, or a string.
type Ref struct {
	Path []string `json:"path"`
	// Of is the kind in the set it names, or "" for one it does not say.
	Of string `json:"of,omitempty"`
}

// Plan says what to make of one CRD of a set.
type Plan struct {
	Kind string `json:"kind"`
	// Role is "component", "trait" (one adding the object to a component) or "skip".
	Role    string   `json:"role"`
	Name    string   `json:"name,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	Options Options  `json:"options,omitempty"`
	// Of is, for a trait, the kind of the component's CRD; "" applies it to any workload.
	Of string `json:"of,omitempty"`
	// Ref is, for a trait, the field set to name the component; Label, the label set to its name.
	Ref   []string `json:"ref,omitempty"`
	Label string   `json:"label,omitempty"`
	// Many makes the trait's parameter a list, adding an object for each entry.
	Many bool `json:"many,omitempty"`
}

// documents are the YAML documents of src, each as its own source.
func documents(src []byte) ([][]byte, error) {
	var out [][]byte
	d := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var n yaml.Node
		if err := d.Decode(&n); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		b, err := yaml.Marshal(&n)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
}

// crds are the CRDs among src's documents, in order, each with its source.
func crds(src []byte) ([]parsed, [][]byte, error) {
	docs, err := documents(src)
	if err != nil {
		return nil, nil, err
	}
	var ps []parsed
	var srcs [][]byte
	for _, d := range docs {
		var head struct {
			Kind string `yaml:"kind"`
		}
		if yaml.Unmarshal(d, &head) != nil || head.Kind != "CustomResourceDefinition" {
			continue
		}
		p, err := parse(d)
		if err != nil {
			return nil, nil, err
		}
		ps = append(ps, p)
		srcs = append(srcs, d)
	}
	if len(ps) == 0 {
		return nil, nil, errors.New("there is no CustomResourceDefinition here")
	}
	return ps, srcs, nil
}

// ReadSet is what each CRD among src's documents offers, as Read says, and
// the fields of each that name another object, the kind they name when it is
// one of the set: by the field's name (cacheRef names a Cache) or its description.
func ReadSet(src []byte) ([]Member, error) {
	ps, _, err := crds(src)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, len(ps))
	for i, p := range ps {
		kinds[i] = p.info.Kind
	}
	out := make([]Member, len(ps))
	for i, p := range ps {
		p.info.Fields = fieldsOf(p.spec, nil, 0)
		out[i] = Member{Info: p.info, Refs: refsOf(p.spec, nil, p.info.Kind, kinds)}
	}
	return out, nil
}

// refNames are fields that name another object whatever they are called.
var refNames = map[string]bool{"cluster": true, "target": true, "owner": true, "parent": true}

// refsOf are the fields of s, two deep, that name another object.
func refsOf(s *schema, path []string, self string, kinds []string) []Ref {
	var out []Ref
	for _, pr := range s.Props {
		p := append(append([]string{}, path...), pr.Name)
		ps := pr.Schema
		lower := strings.ToLower(pr.Name)
		var named *schema
		for _, c := range ps.Props {
			if c.Name == "name" && c.Schema.Type == "string" {
				named = c.Schema
			}
		}
		of := ""
		for _, k := range kinds {
			lk := strings.ToLower(k)
			if k == self {
				continue
			}
			if lower == lk || lower == lk+"ref" || lower == lk+"name" || mentions(ps.Description, k) || (named != nil && mentions(named.Description, k)) {
				of = k
			}
		}
		switch {
		case named != nil && (of != "" || refNames[lower] || strings.HasSuffix(lower, "ref")):
			out = append(out, Ref{Path: p, Of: of})
		case ps.Type == "string" && of != "":
			out = append(out, Ref{Path: p, Of: of})
		case ps.isStruct() && len(path) < 1:
			out = append(out, refsOf(ps, p, self, kinds)...)
		}
	}
	return out
}

// mentions is whether text names kind as a word.
func mentions(text, kind string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(kind) + `\b`).MatchString(text)
}

// GenerateSet makes the definitions plans say for the CRDs among src's
// documents: a component as GenerateWith makes it, or a trait adding the
// object to a component, naming it by a reference or a label.
func GenerateSet(src []byte, plans []Plan) ([]File, error) {
	ps, srcs, err := crds(src)
	if err != nil {
		return nil, err
	}
	find := func(kind string) (int, error) {
		for i, p := range ps {
			if p.info.Kind == kind {
				return i, nil
			}
		}
		return 0, fmt.Errorf("there is no %s among these CRDs", kind)
	}
	var files []File
	made := map[string]bool{}
	for _, pl := range plans {
		if pl.Role == "skip" {
			continue
		}
		i, err := find(pl.Kind)
		if err != nil {
			return nil, err
		}
		var fs []File
		switch pl.Role {
		case "component":
			fs, err = GenerateWith(srcs[i], pl.Name, pl.Choices, pl.Options)
		case "trait":
			var of *Info
			if pl.Of != "" {
				j, err := find(pl.Of)
				if err != nil {
					return nil, err
				}
				of = &ps[j].info
			}
			var f File
			f, err = related(ps[i], of, pl)
			fs = []File{f}
		default:
			err = fmt.Errorf("%s: a role is component, trait or skip, not %q", pl.Kind, pl.Role)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pl.Kind, err)
		}
		for _, f := range fs {
			if made[f.Name] {
				return nil, fmt.Errorf("two definitions would be written to %s", f.Name)
			}
			made[f.Name] = true
		}
		files = append(files, fs...)
	}
	return files, nil
}

// related is a trait adding p's object to the component, as pl says.
func related(p parsed, of *Info, pl Plan) (File, error) {
	if len(pl.Ref) == 0 && pl.Label == "" {
		return File{}, fmt.Errorf("say which field or label points at the component")
	}
	to := map[string]string{}
	for _, c := range pl.Choices {
		to[strings.Join(c.Path, ".")] = c.To
	}
	var ref *schema
	if len(pl.Ref) > 0 {
		ref = at(p.spec, pl.Ref)
		if ref == nil {
			return File{}, fmt.Errorf("its spec has no %s", strings.Join(pl.Ref, "."))
		}
		to[strings.Join(pl.Ref, ".")] = "ref"
	}
	if err := checkRequired(p.spec, nil, "component", to); err != nil {
		return File{}, err
	}
	cut := subsetOf(p.spec, nil, "component", inComponent, to)

	var usesStrings bool
	params := cut.structOf(0, nil, false, &usesStrings)
	kebabKind := kebab(p.info.Kind)
	suffix := kebabKind
	if of != nil {
		suffix = strings.TrimPrefix(kebabKind, kebab(of.Kind)+"-")
	}
	name := "context.name + " + strconv.Quote("-"+suffix)
	key := label(suffix)
	if pl.Many {
		for _, pr := range cut.Props {
			if pr.Name == "name" {
				return File{}, fmt.Errorf("its spec has a name of its own, so each object cannot be named by its entry")
			}
		}
		list := lowerFirst(camel(suffix)) + "s"
		params = fmt.Sprintf("{\n// +usage=Each %s to add\n%s: [...{\n// +usage=Name of the %s\nname: string\n%s]\n}", p.info.Kind, label(list), p.info.Kind, strings.TrimPrefix(cut.structOf(1, nil, false, &usesStrings), "{\n"))
		name, key = "o.name", `"`+suffix+`-\(o.name)"`
	}

	var spec strings.Builder
	if pl.Many {
		spec.WriteString("for k, v in o if k != \"name\" {\n(k): v\n}\n")
	} else {
		spec.WriteString("parameter\n")
	}
	if ref != nil {
		spec.WriteString(refValue(pl.Ref, ref))
	}
	metadata := "metadata: name: " + name
	if pl.Label != "" {
		metadata = "metadata: {\nname: " + name + "\nlabels: " + strconv.Quote(pl.Label) + ": context.name\n}"
	}
	object := fmt.Sprintf("%s: {\napiVersion: %s\nkind: %s\n%s\nspec: {\n%s}\n}\n",
		key, strconv.Quote(p.info.Group+"/"+p.info.Version), strconv.Quote(p.info.Kind), metadata, spec.String())
	if pl.Many {
		object = "for o in parameter." + label(lowerFirst(camel(suffix))+"s") + " {\n" + object + "}\n"
	}

	var applies string
	if of != nil {
		applies = fmt.Sprintf("\t\tappliesToWorkloads: [%s]\n", strconv.Quote(of.Plural+"."+of.Group))
	}
	target := "the component"
	if of != nil {
		target = "a " + of.Kind
	}
	trait := fmt.Sprintf(`%s: {
	type:        "trait"
	description: %s
	attributes: {
%s		podDisruptive: false
%s	}
}
template: {
	outputs: {
%s	}
	parameter: %s
}
`, strconv.Quote(pl.Name), strconv.Quote(fmt.Sprintf("Adds a %s (%s/%s) for %s.", p.info.Kind, p.info.Group, p.info.Version, target)), applies, outputsHealth(p.info.Ready, pl.Options.Condition, pl.Many), object, params)
	if usesStrings {
		trait = "import \"strings\"\n\n" + trait
	}
	text, err := format.Source([]byte(trait))
	if err != nil {
		return File{}, fmt.Errorf("trait %s does not format: %w\n%s", pl.Name, err, trait)
	}
	return File{Name: pl.Name + ".cue", Text: string(text)}, nil
}

// at is the schema of the field at path in s, or nil.
func at(s *schema, path []string) *schema {
	for _, seg := range path {
		var next *schema
		for _, pr := range s.Props {
			if pr.Name == seg {
				next = pr.Schema
			}
		}
		if next == nil {
			return nil
		}
		s = next
	}
	return s
}

// refValue is the reference at path set to name the component: a string to
// its name, or a struct's name, kind and apiVersion to the component's.
func refValue(path []string, ref *schema) string {
	labels := make([]string, len(path))
	for i, seg := range path {
		labels[i] = label(seg)
	}
	head := strings.Join(labels, ": ")
	if !ref.isStruct() {
		return head + ": context.name\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: {\nname: context.name\n", head)
	for _, pr := range ref.Props {
		switch pr.Name {
		case "kind":
			b.WriteString("kind: context.output.kind\n")
		case "apiVersion":
			b.WriteString("apiVersion: context.output.apiVersion\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// outputsHealth is a health policy for a CRD whose status has conditions:
// healthy when every object added has the condition, Ready unless another is named, True.
func outputsHealth(ready bool, condition string, many bool) string {
	if !ready || condition == "-" {
		return ""
	}
	if condition == "" {
		condition = "Ready"
	}
	message := ""
	if many {
		message = `
			customStatus: #"""
				message: "\(len(_ready))/\(len(context.outputs)) ready"
				_ready: [for _, o in context.outputs if o.status.conditions != _|_ for c in o.status.conditions if c.type == CONDITION && c.status == "True" {true}]
				"""#`
	}
	return strings.ReplaceAll(`		status: {
			healthPolicy: #"""
				isHealth: len(_ready) == len(context.outputs)
				_ready: [for _, o in context.outputs if o.status.conditions != _|_ for c in o.status.conditions if c.type == CONDITION && c.status == "True" {true}]
				"""#`+message+`
		}
`, "CONDITION", strconv.Quote(condition))
}

// kebab is a kind as a definition name: CacheBackup is cache-backup.
func kebab(s string) string {
	return strings.ToLower(regexp.MustCompile(`([a-z0-9])([A-Z])`).ReplaceAllString(s, "$1-$2"))
}

// camel is a kebab name as camel case: scheduled-backup is scheduledBackup.
func camel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
