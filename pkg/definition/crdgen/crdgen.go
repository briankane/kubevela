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

// Package crdgen makes X-Definitions from a CustomResourceDefinition: a
// component rendering the custom resource, its spec as the parameter, and
// traits patching the parts of the spec chosen for them.
package crdgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue/format"
	"gopkg.in/yaml.v3"
)

// Info is what a CRD offers to make definitions from.
type Info struct {
	Kind    string `json:"kind"`
	Group   string `json:"group"`
	Version string `json:"version"`
	Plural  string `json:"plural"`
	// Versions are those the CRD serves; Version is its storage version.
	Versions []string `json:"versions"`
	// Ready says its status has conditions, which a health policy can read.
	Ready  bool    `json:"ready"`
	Fields []Field `json:"fields"`
}

// Field is a field of the custom resource's spec.
type Field struct {
	Name        string   `json:"name"`
	Path        []string `json:"path"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	// Hint says why the field is often set by a trait, if it is.
	Hint string `json:"hint,omitempty"`
	// Merge says how a trait's patch merges it, when the CRD says.
	Merge    string  `json:"merge,omitempty"`
	Children []Field `json:"children,omitempty"`
}

// Choice says where a spec field, and the fields under it unless they have a
// choice of their own, goes: "component", "trait:<name>", "both:<name>" (the
// component sets it and the trait overrides it) or "omit".
type Choice struct {
	Path []string `json:"path"`
	To   string   `json:"to"`
}

// File is a definition file made.
type File struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// operational are spec fields usually set apart from what a resource is.
var operational = map[string]string{
	"resources":                 "resource requests and limits are often a trait",
	"affinity":                  "scheduling is often a trait",
	"tolerations":               "scheduling is often a trait",
	"nodeSelector":              "scheduling is often a trait",
	"topologySpreadConstraints": "scheduling is often a trait",
	"priorityClassName":         "scheduling is often a trait",
	"securityContext":           "security settings are often a trait",
	"podSecurityContext":        "security settings are often a trait",
	"imagePullSecrets":          "registry access is often a trait",
	"podTemplate":               "pod settings are often a trait",
	"monitoring":                "monitoring is often a trait",
	"metrics":                   "monitoring is often a trait",
	"serviceMonitor":            "monitoring is often a trait",
	"podMonitor":                "monitoring is often a trait",
	"sidecars":                  "sidecars are often a trait",
}

// crd is the part of a CustomResourceDefinition read.
type crd struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Group string `yaml:"group"`
		Names struct {
			Kind   string `yaml:"kind"`
			Plural string `yaml:"plural"`
		} `yaml:"names"`
		Versions []struct {
			Name    string `yaml:"name"`
			Served  bool   `yaml:"served"`
			Storage bool   `yaml:"storage"`
			Schema  struct {
				OpenAPIV3Schema *schema `yaml:"openAPIV3Schema"`
			} `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"spec"`
}

// parsed is a CRD's storage version: its info, and the schema of its spec.
type parsed struct {
	info Info
	spec *schema
}

func parse(src []byte) (parsed, error) {
	var c crd
	if err := yaml.Unmarshal(src, &c); err != nil {
		return parsed{}, err
	}
	if c.Kind != "CustomResourceDefinition" {
		return parsed{}, fmt.Errorf("this is a %s, not a CustomResourceDefinition", orNothing(c.Kind))
	}
	p := parsed{info: Info{Kind: c.Spec.Names.Kind, Group: c.Spec.Group, Plural: c.Spec.Names.Plural}}
	var root *schema
	for _, v := range c.Spec.Versions {
		if v.Served {
			p.info.Versions = append(p.info.Versions, v.Name)
		}
		if v.Storage || root == nil {
			p.info.Version, root = v.Name, v.Schema.OpenAPIV3Schema
		}
	}
	if root == nil {
		return parsed{}, fmt.Errorf("%s has no schema to make a parameter from", c.Spec.Names.Kind)
	}
	for _, pr := range root.Props {
		switch pr.Name {
		case "spec":
			p.spec = pr.Schema
		case "status":
			for _, sp := range pr.Schema.Props {
				if sp.Name == "conditions" && sp.Schema.Items != nil && hasProps(sp.Schema.Items, "type", "status") {
					p.info.Ready = true
				}
			}
		}
	}
	if p.spec == nil {
		p.spec = &schema{Type: "object"}
	}
	return p, nil
}

func orNothing(s string) string {
	if s == "" {
		return "file with no kind"
	}
	return s
}

func hasProps(s *schema, names ...string) bool {
	for _, n := range names {
		found := false
		for _, p := range s.Props {
			found = found || p.Name == n
		}
		if !found {
			return false
		}
	}
	return true
}

// Read is what a CRD offers: its kind and storage version, and its spec's
// fields, structs with their own, in the schema's order.
func Read(src []byte) (Info, error) {
	p, err := parse(src)
	if err != nil {
		return Info{}, err
	}
	p.info.Fields = fieldsOf(p.spec, nil, 0)
	return p.info, nil
}

func fieldsOf(s *schema, path []string, depth int) []Field {
	var out []Field
	for _, pr := range s.Props {
		ps := pr.Schema
		f := Field{
			Name:        pr.Name,
			Path:        append(append([]string{}, path...), pr.Name),
			Required:    s.required(pr.Name),
			Description: usage(ps.Description),
		}
		if depth == 0 {
			f.Hint = operational[pr.Name]
		}
		if ps.HasDefault {
			f.Default = literal(ps.Default)
		}
		f.Merge = ps.merge()
		var usesStrings bool
		if ps.isStruct() && depth < maxDepth {
			f.Type = "{…}"
			f.Children = fieldsOf(ps, f.Path, depth+1)
		} else {
			f.Type = ps.cueType(depth+1, &usesStrings)
			if strings.Contains(f.Type, "\n") {
				f.Type = "{…}"
				if ps.Type == "array" {
					f.Type = "[...{…}]"
				}
			}
		}
		out = append(out, f)
	}
	return out
}

// Options are the choices Generate takes beyond where each field goes.
type Options struct {
	// Condition is the status condition type the component's health reads,
	// for a CRD whose status has conditions: Ready when empty, none when "-".
	Condition string `json:"condition,omitempty"`
}

// Generate is GenerateWith the default options.
func Generate(src []byte, name string, choices []Choice) ([]File, error) {
	return GenerateWith(src, name, choices, Options{})
}

// GenerateWith makes the definitions for a CRD: a component named name whose
// output is the custom resource with the spec fields chosen for it as its
// parameter, and a trait for each trait named in choices, patching its
// fields of the spec. A field with no choice goes where its parent goes,
// and a top-level field to the component; a required one may only go there.
func GenerateWith(src []byte, name string, choices []Choice, opts Options) ([]File, error) {
	p, err := parse(src)
	if err != nil {
		return nil, err
	}
	to := map[string]string{}
	traits := map[string]bool{}
	for _, c := range choices {
		to[strings.Join(c.Path, ".")] = c.To
		if t, ok := traitOf(c.To); ok {
			traits[t] = true
		}
	}
	if err := checkRequired(p.spec, nil, "component", to); err != nil {
		return nil, err
	}
	apiVersion := p.info.Group + "/" + p.info.Version
	var usesStrings bool
	body := p.spec.structOf(0, subset(p.spec, nil, "component", inComponent, to), false, &usesStrings)
	component := fmt.Sprintf(`%s: {
	type:        "component"
	description: %s
	attributes: {
		workload: definition: {
			apiVersion: %s
			kind:       %s
		}
%s	}
}
template: {
	output: {
		apiVersion: %s
		kind:       %s
		metadata: name: context.name
		spec: parameter
	}
	parameter: %s
}
`, strconv.Quote(name), strconv.Quote(fmt.Sprintf("%s (%s), from its CRD.", p.info.Kind, apiVersion)), strconv.Quote(apiVersion), strconv.Quote(p.info.Kind), healthPolicy(p.info.Ready, opts.Condition), strconv.Quote(apiVersion), strconv.Quote(p.info.Kind), body)
	if usesStrings {
		component = "import \"strings\"\n\n" + component
	}
	files := []File{}
	text, err := format.Source([]byte(component))
	if err != nil {
		return nil, fmt.Errorf("the component does not format: %w", err)
	}
	files = append(files, File{Name: name + ".cue", Text: string(text)})

	names := make([]string, 0, len(traits))
	for t := range traits {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		var usesStrings bool
		// A trait sets only what it is given, so its top-level fields are optional.
		cut := subsetOf(p.spec, nil, "component", inTrait(t), to)
		params := cut.structOf(0, nil, true, &usesStrings)
		patch := "patch: spec: parameter"
		if body, explicit := patchOf(cut, nil, "component", t, to); explicit {
			patch = "patch: spec: {\n" + body + "}"
		}
		trait := fmt.Sprintf(`%s: {
	type:        "trait"
	description: %s
	attributes: {
		appliesToWorkloads: [%s]
		podDisruptive: false
	}
}
template: {
	%s
	parameter: %s
}
`, strconv.Quote(t), strconv.Quote(fmt.Sprintf("Sets part of a %s's spec.", p.info.Kind)), strconv.Quote(p.info.Plural+"."+p.info.Group), patch, params)
		if usesStrings {
			trait = "import \"strings\"\n\n" + trait
		}
		text, err := format.Source([]byte(trait))
		if err != nil {
			return nil, fmt.Errorf("trait %s does not format: %w", t, err)
		}
		files = append(files, File{Name: t + ".cue", Text: string(text)})
	}
	return files, nil
}

// destination is where the field at path goes: its own choice, or its parent's.
func destination(path []string, parent string, to map[string]string) string {
	if d, ok := to[strings.Join(path, ".")]; ok {
		return d
	}
	return parent
}

// checkRequired refuses a required field sent anywhere but where its parent
// goes, other than the reference a related object's trait sets ("ref").
func checkRequired(s *schema, path []string, parent string, to map[string]string) error {
	for _, pr := range s.Props {
		p := append(append([]string{}, path...), pr.Name)
		d := destination(p, parent, to)
		overridden := strings.HasPrefix(d, "both:") && inComponent(parent)
		if s.required(pr.Name) && d != parent && !overridden && d != "ref" {
			return fmt.Errorf("%s is required, so it goes where its parent goes (%s), or stays and is overridden", strings.Join(p, "."), parent)
		}
		if err := checkRequired(pr.Schema, p, d, to); err != nil {
			return err
		}
	}
	return nil
}

// traitOf is the trait a destination names, if it names one.
func traitOf(d string) (string, bool) {
	for _, prefix := range []string{"trait:", "both:"} {
		if strings.HasPrefix(d, prefix) {
			return strings.TrimPrefix(d, prefix), true
		}
	}
	return "", false
}

// inComponent is whether a destination puts a field in the component.
func inComponent(d string) bool {
	return d == "component" || strings.HasPrefix(d, "both:")
}

// inTrait is whether a destination puts a field in trait t.
func inTrait(t string) func(string) bool {
	return func(d string) bool { return d == "trait:"+t || d == "both:"+t }
}

// subset says which properties of an object go where want says, and their
// schema cut to what goes there.
func subset(s *schema, path []string, parent string, want func(string) bool, to map[string]string) func(string) (*schema, bool) {
	cut := subsetOf(s, path, parent, want, to)
	return func(name string) (*schema, bool) {
		for _, pr := range cut.Props {
			if pr.Name == name {
				return pr.Schema, true
			}
		}
		return nil, false
	}
}

// subsetOf is an object's schema cut to the properties that go where want
// says: a struct is kept when any field under it is. A struct only part of
// which goes there requires nothing, since the rest is set elsewhere, and a
// field another destination also sets has no default there, so it overrides
// only when given.
func subsetOf(s *schema, path []string, parent string, want func(string) bool, to map[string]string) *schema {
	cut := *s
	cut.Props = nil
	for _, pr := range s.Props {
		p := append(append([]string{}, path...), pr.Name)
		d := destination(p, parent, to)
		ps := pr.Schema
		if ps.isStruct() {
			sub := subsetOf(ps, p, d, want, to)
			if len(sub.Props) == 0 {
				continue
			}
			if !want(d) {
				sub.Required, sub.HasDefault = nil, false
			}
			cut.Props = append(cut.Props, prop{Name: pr.Name, Schema: sub})
			continue
		}
		if !want(d) {
			continue
		}
		if strings.HasPrefix(d, "both:") && !want("component") {
			trimmed := *ps
			trimmed.HasDefault, trimmed.Default = false, nil
			ps = &trimmed
		}
		cut.Props = append(cut.Props, prop{Name: pr.Name, Schema: ps})
	}
	return &cut
}

// patchOf is a trait's patch of the spec, written out a field at a time when
// any field of it needs a marker: one that overrides the component's value
// (+patchStrategy=retainKeys, or replace for a list), or one whose CRD says
// how it merges. Each field is guarded, so one not given patches nothing.
// explicit is false when the plain spec: parameter patches as well.
func patchOf(cut *schema, path []string, parent, trait string, to map[string]string) (body string, explicit bool) {
	var b strings.Builder
	for _, pr := range cut.Props {
		p := append(append([]string{}, path...), pr.Name)
		d := destination(p, parent, to)
		ps := pr.Schema
		marker := ps.marker()
		if d == "both:"+trait && d != parent {
			marker = "+patchStrategy=retainKeys"
			if ps.Type == "array" {
				marker = "+patchStrategy=replace"
			}
		}
		ref := "parameter"
		for _, seg := range p {
			ref += "." + label(seg)
		}
		fmt.Fprintf(&b, "if %s != _|_ {\n", ref)
		switch {
		case marker != "":
			explicit = true
			fmt.Fprintf(&b, "// %s\n%s: %s\n", marker, label(pr.Name), ref)
		case ps.isStruct():
			inner, sub := patchOf(ps, p, d, trait, to)
			if sub {
				explicit = true
				fmt.Fprintf(&b, "%s: {\n%s}\n", label(pr.Name), inner)
			} else {
				fmt.Fprintf(&b, "%s: %s\n", label(pr.Name), ref)
			}
		default:
			fmt.Fprintf(&b, "%s: %s\n", label(pr.Name), ref)
		}
		b.WriteString("}\n")
	}
	return b.String(), explicit
}

// healthPolicy reads a status condition, Ready unless another is named, for
// a CRD whose status has conditions.
func healthPolicy(ready bool, condition string) string {
	if !ready || condition == "-" {
		return ""
	}
	if condition == "" {
		condition = "Ready"
	}
	return strings.ReplaceAll(`		status: {
			healthPolicy: #"""
				isHealth: *false | bool
				if context.output.status.conditions != _|_ {
					for c in context.output.status.conditions if c.type == CONDITION {
						isHealth: c.status == "True"
					}
				}
				"""#
			customStatus: #"""
				message: *"" | string
				if context.output.status.conditions != _|_ {
					for c in context.output.status.conditions if c.type == CONDITION && c.message != _|_ {
						message: c.message
					}
				}
				"""#
		}
`, "CONDITION", strconv.Quote(condition))
}
