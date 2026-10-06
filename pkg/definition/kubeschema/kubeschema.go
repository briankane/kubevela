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

// Package kubeschema turns the OpenAPI schemas of Kubernetes kinds into CUE
// definitions, so a value can be checked against its kind by unifying it with
// one. Schemas come from OpenAPI v3 documents, as a cluster serves them at
// /openapi/v3, and from CustomResourceDefinitions.
package kubeschema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// GVK names a kind.
type GVK struct {
	Group, Version, Kind string
}

// ParseGVK is the kind an object's apiVersion and kind name.
func ParseGVK(apiVersion, kind string) GVK {
	group, version, found := strings.Cut(apiVersion, "/")
	if !found {
		group, version = "", apiVersion
	}
	return GVK{Group: group, Version: version, Kind: kind}
}

// Root is the CUE definition CUE(gvk) declares for the kind.
func Root(gvk GVK) string {
	return "#kubekind_" + sanitize(gvk.Group+"_"+gvk.Version+"_"+gvk.Kind)
}

// schema is one OpenAPI schema, as decoded from JSON.
type schema = map[string]any

// Schemas is the schemas of a set of kinds. A source added later replaces
// what an earlier one said about the same kind. It is safe for concurrent use.
type Schemas struct {
	mu        sync.Mutex
	defs      map[string]schema
	kinds     map[GVK]string
	resources map[string]GVK
	converted map[GVK]string
}

// New returns an empty set.
func New() *Schemas {
	return &Schemas{defs: map[string]schema{}, kinds: map[GVK]string{}, resources: map[string]GVK{}, converted: map[GVK]string{}}
}

// AddDocument adds the kinds of an OpenAPI v3 document: each component
// schema marked with x-kubernetes-group-version-kind.
func (s *Schemas) AddDocument(data []byte) error {
	var doc struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("reading an OpenAPI v3 document: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, sch := range doc.Components.Schemas {
		s.defs[name] = sch
		gvks, _ := sch["x-kubernetes-group-version-kind"].([]any)
		for _, g := range gvks {
			m, _ := g.(map[string]any)
			group, _ := m["group"].(string)
			version, _ := m["version"].(string)
			kind, _ := m["kind"].(string)
			if version != "" && kind != "" {
				s.kinds[GVK{Group: group, Version: version, Kind: kind}] = name
			}
		}
	}
	s.converted = map[GVK]string{}
	return nil
}

// AddCRD adds the kinds a CustomResourceDefinition, YAML or JSON, declares:
// one per version with a schema.
func (s *Schemas) AddCRD(data []byte) error {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		return fmt.Errorf("reading a CustomResourceDefinition: %w", err)
	}
	if crd.Kind != "CustomResourceDefinition" {
		return fmt.Errorf("not a CustomResourceDefinition: kind %q", crd.Kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range crd.Spec.Versions {
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		raw, err := json.Marshal(v.Schema.OpenAPIV3Schema)
		if err != nil {
			return err
		}
		var sch schema
		if err := json.Unmarshal(raw, &sch); err != nil {
			return err
		}
		withObjectFields(sch)
		gvk := GVK{Group: crd.Spec.Group, Version: v.Name, Kind: crd.Spec.Names.Kind}
		name := "crd." + gvk.Group + "." + gvk.Version + "." + gvk.Kind
		s.defs[name] = sch
		s.kinds[gvk] = name
		if v.Storage || s.resources[crd.Spec.Names.Plural+"."+gvk.Group] == (GVK{}) {
			s.resources[crd.Spec.Names.Plural+"."+gvk.Group] = gvk
		}
	}
	s.converted = map[GVK]string{}
	return nil
}

// withObjectFields declares the fields every object has, which a CRD's schema
// leaves out.
func withObjectFields(root schema) {
	props, _ := root["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		root["properties"] = props
	}
	for _, f := range []string{"apiVersion", "kind"} {
		if _, ok := props[f]; !ok {
			props[f] = schema{"type": "string"}
		}
	}
	if m, ok := props["metadata"].(map[string]any); !ok || len(m) <= 1 {
		props["metadata"] = schema{"type": "object", "x-kubernetes-preserve-unknown-fields": true}
	}
}

// Has reports whether the set has the kind's schema.
func (s *Schemas) Has(gvk GVK) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.kinds[gvk]
	return ok
}

// KindOf is the kind of a resource named as "<plural>.<group>", as a trait's
// appliesToWorkloads names it, or the zero GVK when no CRD declares it.
func (s *Schemas) KindOf(resource string) GVK {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resources[resource]
}

// CUE is CUE source declaring Root(gvk), the kind's schema, and every
// definition it refers to.
func (s *Schemas) CUE(gvk GVK) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if src, ok := s.converted[gvk]; ok {
		return src, true
	}
	name, ok := s.kinds[gvk]
	if !ok {
		return "", false
	}
	c := &converter{defs: s.defs, emitted: map[string]bool{}}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", Root(gvk), c.ref(name))
	for len(c.queue) > 0 {
		next := c.queue[0]
		c.queue = c.queue[1:]
		fmt.Fprintf(&b, "%s: %s\n", defName(next), c.typ(s.defs[next]))
	}
	src := b.String()
	s.converted[gvk] = src
	return src, true
}

// converter writes schemas as CUE, queueing each referenced schema once.
type converter struct {
	defs    map[string]schema
	emitted map[string]bool
	queue   []string
}

func (c *converter) ref(name string) string {
	if _, ok := c.defs[name]; !ok {
		return "_"
	}
	if !c.emitted[name] {
		c.emitted[name] = true
		c.queue = append(c.queue, name)
	}
	return defName(name)
}

func (c *converter) typ(s schema) string {
	t := c.baseType(s)
	if nullable, _ := s["nullable"].(bool); nullable {
		t = "(" + t + ") | null"
	}
	return t
}

func (c *converter) baseType(s schema) string {
	if ref, ok := s["$ref"].(string); ok {
		return c.ref(strings.TrimPrefix(ref, "#/components/schemas/"))
	}
	if all, ok := s["allOf"].([]any); ok && len(all) > 0 {
		parts := make([]string, 0, len(all))
		for _, a := range all {
			if m, ok := a.(map[string]any); ok {
				parts = append(parts, c.typ(m))
			}
		}
		return strings.Join(parts, " & ")
	}
	if ios, _ := s["x-kubernetes-int-or-string"].(bool); ios || s["format"] == "int-or-string" {
		return "int | string"
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		alts := make([]string, 0, len(enum))
		for _, e := range enum {
			lit, err := json.Marshal(e)
			if err == nil {
				alts = append(alts, string(lit))
			}
		}
		return strings.Join(alts, " | ")
	}
	typ, _ := s["type"].(string)
	_, hasProps := s["properties"]
	// oneOf and anyOf give a type only when nothing else does; in a CRD they
	// usually only constrain which fields are set.
	if typ == "" && !hasProps {
		for _, key := range []string{"oneOf", "anyOf"} {
			if alts, ok := s[key].([]any); ok && len(alts) > 0 {
				parts := make([]string, 0, len(alts))
				for _, a := range alts {
					if m, ok := a.(map[string]any); ok {
						parts = append(parts, c.typ(m))
					}
				}
				return strings.Join(parts, " | ")
			}
		}
	}
	switch {
	case typ == "object" || hasProps:
		return c.object(s)
	case typ == "array":
		if items, ok := s["items"].(map[string]any); ok {
			return "[..." + c.typ(items) + "]"
		}
		return "[...]"
	case typ == "string":
		return "string"
	case typ == "integer":
		return "int"
	case typ == "number":
		return "number"
	case typ == "boolean":
		return "bool"
	}
	return "_"
}

func (c *converter) object(s schema) string {
	props, _ := s["properties"].(map[string]any)
	open, _ := s["x-kubernetes-preserve-unknown-fields"].(bool)
	embedded, _ := s["x-kubernetes-embedded-resource"].(bool)
	if len(props) == 0 {
		switch ap := s["additionalProperties"].(type) {
		case map[string]any:
			return "{[string]: " + c.typ(ap) + "}"
		case bool:
			if !ap {
				return "{}"
			}
		}
		return "{...}"
	}
	required := map[string]bool{}
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				required[name] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("{")
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		mark := "?"
		if required[n] {
			mark = "!"
		}
		fmt.Fprintf(&b, "%s%s: %s, ", strconv.Quote(n), mark, c.typ(p))
	}
	if open || embedded {
		b.WriteString("...")
	} else if ap, ok := s["additionalProperties"].(map[string]any); ok {
		b.WriteString("[string]: " + c.typ(ap))
	}
	b.WriteString("}")
	return b.String()
}

func defName(name string) string {
	return "#kube_" + sanitize(name)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}
