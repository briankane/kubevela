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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// schema is the part of a CRD's OpenAPI v3 schema a parameter is made from.
// It is read from YAML nodes, so properties keep the order they are written in.
type schema struct {
	Type            string
	Description     string
	Default         interface{}
	HasDefault      bool
	Enum            []interface{}
	Required        []string
	Props           []prop
	Items           *schema
	Additional      *schema
	PreserveUnknown bool
	IntOrString     bool
	Minimum         *float64
	Maximum         *float64
	ExclusiveMin    bool
	ExclusiveMax    bool
	MinLength       *int64
	MaxLength       *int64
	Pattern         string
}

// prop is a property of an object schema.
type prop struct {
	Name   string
	Schema *schema
}

// UnmarshalYAML reads a schema from its node.
func (s *schema) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a schema is a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, v := n.Content[i].Value, n.Content[i+1]
		var err error
		switch key {
		case "type":
			s.Type = v.Value
		case "description":
			s.Description = v.Value
		case "default":
			s.HasDefault = true
			err = v.Decode(&s.Default)
		case "enum":
			err = v.Decode(&s.Enum)
		case "required":
			err = v.Decode(&s.Required)
		case "properties":
			if v.Kind != yaml.MappingNode {
				return fmt.Errorf("line %d: properties is a mapping", v.Line)
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				p := &schema{}
				if err := v.Content[j+1].Decode(p); err != nil {
					return err
				}
				s.Props = append(s.Props, prop{Name: v.Content[j].Value, Schema: p})
			}
		case "items":
			if v.Kind == yaml.MappingNode {
				s.Items = &schema{}
				err = v.Decode(s.Items)
			}
		case "additionalProperties":
			if v.Kind == yaml.MappingNode {
				s.Additional = &schema{}
				err = v.Decode(s.Additional)
			}
		case "x-kubernetes-preserve-unknown-fields":
			s.PreserveUnknown = v.Value == "true"
		case "x-kubernetes-int-or-string":
			s.IntOrString = v.Value == "true"
		case "minimum":
			s.Minimum, err = number(v)
		case "maximum":
			s.Maximum, err = number(v)
		case "exclusiveMinimum":
			s.ExclusiveMin = v.Value == "true"
		case "exclusiveMaximum":
			s.ExclusiveMax = v.Value == "true"
		case "minLength":
			s.MinLength, err = integer(v)
		case "maxLength":
			s.MaxLength, err = integer(v)
		case "pattern":
			s.Pattern = v.Value
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func number(n *yaml.Node) (*float64, error) {
	f, err := strconv.ParseFloat(n.Value, 64)
	return &f, err
}

func integer(n *yaml.Node) (*int64, error) {
	i, err := strconv.ParseInt(n.Value, 10, 64)
	return &i, err
}

// isStruct is whether the schema is an object with fields of its own.
func (s *schema) isStruct() bool {
	return len(s.Props) > 0
}

// required is whether name is one of the object's required properties.
func (s *schema) required(name string) bool {
	for _, r := range s.Required {
		if r == name {
			return true
		}
	}
	return false
}

// maxDepth bounds how deep a schema is written out; deeper is any value.
const maxDepth = 8

// cueType is the CUE a value of the schema is, its default not included.
// usesStrings is set when it needs the strings package.
func (s *schema) cueType(depth int, usesStrings *bool) string {
	if depth > maxDepth {
		return "_"
	}
	if s.IntOrString {
		return "int | string"
	}
	if len(s.Enum) > 0 {
		alts := make([]string, 0, len(s.Enum))
		for _, e := range s.Enum {
			alts = append(alts, literal(e))
		}
		return strings.Join(alts, " | ")
	}
	switch s.Type {
	case "string":
		parts := []string{}
		if s.MinLength != nil {
			parts = append(parts, fmt.Sprintf("strings.MinRunes(%d)", *s.MinLength))
		}
		if s.MaxLength != nil {
			parts = append(parts, fmt.Sprintf("strings.MaxRunes(%d)", *s.MaxLength))
		}
		if len(parts) > 0 {
			*usesStrings = true
		}
		if s.Pattern != "" {
			parts = append(parts, "=~"+strconv.Quote(s.Pattern))
		}
		if len(parts) == 0 || s.Pattern != "" && len(parts) == 1 {
			parts = append([]string{"string"}, parts...)
		}
		return strings.Join(parts, " & ")
	case "integer", "number":
		t := "int"
		if s.Type == "number" {
			t = "number"
		}
		parts := []string{t}
		if s.Minimum != nil {
			op := ">="
			if s.ExclusiveMin {
				op = ">"
			}
			parts = append(parts, op+num(*s.Minimum))
		}
		if s.Maximum != nil {
			op := "<="
			if s.ExclusiveMax {
				op = "<"
			}
			parts = append(parts, op+num(*s.Maximum))
		}
		return strings.Join(parts, " & ")
	case "boolean":
		return "bool"
	case "array":
		if s.Items == nil {
			return "[...]"
		}
		return "[..." + s.Items.cueType(depth+1, usesStrings) + "]"
	case "object", "":
		switch {
		case s.isStruct():
			return s.structOf(depth, nil, false, usesStrings)
		case s.Additional != nil:
			return "[string]: " + s.Additional.cueType(depth+1, usesStrings)
		case s.Type == "object" || s.PreserveUnknown:
			return "{...}"
		}
	}
	return "_"
}

// structOf is the CUE struct of an object schema's properties, those keep
// says to, each with its +usage line, its default, and ? unless required, or
// on every one when allOptional.
func (s *schema) structOf(depth int, keep func(name string) (*schema, bool), allOptional bool, usesStrings *bool) string {
	var b strings.Builder
	b.WriteString("{\n")
	for _, p := range s.Props {
		ps := p.Schema
		if keep != nil {
			var ok bool
			if ps, ok = keep(p.Name); !ok {
				continue
			}
		}
		if u := usage(ps.Description); u != "" {
			fmt.Fprintf(&b, "// +usage=%s\n", u)
		}
		mark := "?"
		if !allOptional && (s.required(p.Name) || ps.HasDefault) {
			mark = ""
		}
		t := ps.cueType(depth+1, usesStrings)
		if d := literal(ps.Default); ps.HasDefault && !ps.isStruct() && d != "" {
			t = withDefault(t, d, len(ps.Enum) > 0)
		}
		fmt.Fprintf(&b, "%s%s: %s\n", label(p.Name), mark, t)
	}
	b.WriteString("}")
	return b.String()
}

// withDefault marks d as a type's default: the enum value it is, or ahead of the type.
func withDefault(t, d string, enum bool) string {
	if enum {
		alts := strings.Split(t, " | ")
		for i, a := range alts {
			if a == d {
				alts[i] = "*" + a
				return strings.Join(alts, " | ")
			}
		}
	}
	return "*" + d + " | " + t
}

// usage is a description's first sentence, on one line.
func usage(d string) string {
	d = strings.Join(strings.Fields(d), " ")
	if i := strings.Index(d, ". "); i >= 0 {
		d = d[:i+1]
	}
	return strings.TrimSuffix(d, ".")
}

// literal is a JSON value as a CUE literal, or "" for one that is not a scalar.
func literal(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case bool, int, int64, float64:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// label is a property name as a CUE label: bare when it can be.
func label(name string) string {
	if name == "" {
		return `""`
	}
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return strconv.Quote(name)
		}
	}
	if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "#") {
		return strconv.Quote(name)
	}
	return name
}
