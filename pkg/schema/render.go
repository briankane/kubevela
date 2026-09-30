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

package schema

import (
	"context"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/kubevela/pkg/cue/cuex"

	"github.com/kubevela/pkg/util/stringtools"
	"github.com/oam-dev/kubevela/pkg/cue/process"
	uischema "github.com/oam-dev/kubevela/pkg/utils/schema"
	"github.com/oam-dev/kubevela/pkg/workflow/providers"
)

// schemaContext declares the context fields a parameter default may read, so
// `*context.namespace | string` resolves to a string rather than an undefined
// reference.
const schemaContext = `
context: {
	name:             string
	namespace:        string
	cluster:          string
	appName:          string
	appRevision:      string
	appRevisionNum:   int
	appLabels: [string]:      string
	appAnnotations: [string]: string
	publishVersion:   string
	workflowName:     string
	outputSecretName: string
	revision:         string
	componentName:    string
	componentType:    string
	traitType:        string
	stepName:         string
	stepType:         string
	replicaKey:       string
	policyName:       string
	policyType:       string
	config?: [...{
		name:  string
		value: string
	}]
	...
}
`

// ParameterSchemas are the two schemas generated from a template's parameter.
type ParameterSchemas struct {
	OpenAPI *openapi3.Schema
	UI      uischema.UISchema
}

// GenerateParameterSchemas reads a template's `parameter` into both its
// OpenAPI schema and the default UI schema VelaUX renders a form from.
func GenerateParameterSchemas(ctx context.Context, template string) (*ParameterSchemas, error) {
	src := template
	if pruned, err := PruneToParameter(template); err == nil {
		src = pruned
	}
	full := src + "\n" + schemaContext
	val := cuecontext.New().CompileString(full)
	if val.Err() != nil {
		var err error
		val, err = providers.DefaultCompiler.Get().CompileStringWithOptions(ctx, full, cuex.DisableResolveProviderFunctions{})
		if err != nil {
			return nil, err
		}
	}
	return GenerateParameterSchemasFromValue(val, src)
}

// GenerateParameterSchemasFromValue is GenerateParameterSchemas for a template
// already compiled, such as one that extends another.
func GenerateParameterSchemasFromValue(val cue.Value, src string) (*ParameterSchemas, error) {
	param := val.LookupPath(cue.ParsePath(process.ParameterFieldName))
	model := &Field{Kind: KindObject}
	if param.Exists() {
		var err error
		if model, err = BuildParameter(param, src); err != nil {
			return nil, err
		}
	}
	return &ParameterSchemas{OpenAPI: model.OpenAPI(), UI: model.UIParameters()}, nil
}

// OpenAPI renders the field as an OpenAPI 3.0 schema.
func (f *Field) OpenAPI() *openapi3.Schema {
	s := &openapi3.Schema{Title: f.Name, Description: f.Description, Default: f.Default, Nullable: f.Nullable}
	if f.Immutable {
		s.Extensions = map[string]any{ExtensionImmutable: true}
	}
	switch f.Kind {
	case KindString:
		s.Type = &openapi3.Types{openapi3.TypeString}
	case KindBytes:
		s.Type, s.Format = &openapi3.Types{openapi3.TypeString}, "binary"
	case KindInt:
		s.Type = &openapi3.Types{openapi3.TypeInteger}
	case KindNumber:
		s.Type = &openapi3.Types{openapi3.TypeNumber}
	case KindBool:
		s.Type = &openapi3.Types{openapi3.TypeBoolean}
	case KindArray:
		s.Type = &openapi3.Types{openapi3.TypeArray}
		items := &openapi3.Schema{}
		if f.Items != nil {
			items = f.Items.OpenAPI()
			items.Title = ""
		}
		s.Items = openapi3.NewSchemaRef("", items)
	case KindMap:
		s.Type = &openapi3.Types{openapi3.TypeObject}
		values := &openapi3.Schema{}
		if f.Values != nil {
			values = f.Values.OpenAPI()
			values.Title = ""
		}
		s.AdditionalProperties = openapi3.AdditionalProperties{Schema: openapi3.NewSchemaRef("", values)}
	case KindObject:
		s.Type = &openapi3.Types{openapi3.TypeObject}
		if f.Recursive {
			open := true
			s.AdditionalProperties = openapi3.AdditionalProperties{Has: &open}
			break
		}
		switch {
		case f.Values != nil:
			values := f.Values.OpenAPI()
			values.Title = ""
			s.AdditionalProperties = openapi3.AdditionalProperties{Schema: openapi3.NewSchemaRef("", values)}
		case f.Open:
			s.AdditionalProperties = openapi3.AdditionalProperties{Schema: openapi3.NewSchemaRef("", &openapi3.Schema{})}
		}
		s.Properties = openapi3.Schemas{}
		for _, c := range f.Fields {
			s.Properties[c.Name] = openapi3.NewSchemaRef("", c.OpenAPI())
			if !c.Optional && c.Condition == nil {
				s.Required = append(s.Required, c.Name)
			}
		}
		if f.Discriminator != "" {
			s.OneOf = f.branches()
			s.Discriminator = &openapi3.Discriminator{PropertyName: f.Discriminator}
		}
	case KindOneOf:
		if f.allObjects() {
			// A choice between structs keeps the layout existing readers
			// expect: every property at the top, each branch naming the ones
			// it requires.
			s.Type = &openapi3.Types{openapi3.TypeObject}
			s.Properties = openapi3.Schemas{}
			for _, c := range f.formFields() {
				s.Properties[c.Name] = openapi3.NewSchemaRef("", c.OpenAPI())
			}
			for _, v := range f.Variants {
				b := &openapi3.Schema{}
				for _, c := range v.Fields {
					if !c.Optional && c.Condition == nil {
						b.Required = append(b.Required, c.Name)
					}
				}
				s.OneOf = append(s.OneOf, openapi3.NewSchemaRef("", b))
			}
			break
		}
		for _, v := range f.Variants {
			vs := v.OpenAPI()
			vs.Title = ""
			s.OneOf = append(s.OneOf, openapi3.NewSchemaRef("", vs))
		}
	}
	s.Enum = f.Enum
	s.Min, s.Max, s.ExclusiveMin, s.ExclusiveMax = f.Min, f.Max, f.ExclusiveMin, f.ExclusiveMax
	s.MultipleOf, s.Pattern, s.UniqueItems = f.MultipleOf, f.Pattern, f.UniqueItems
	if f.MinLength != nil {
		s.MinLength = *f.MinLength
	}
	s.MaxLength = f.MaxLength
	if f.MinItems != nil {
		s.MinItems = *f.MinItems
	}
	s.MaxItems = f.MaxItems
	if f.MinProperties != nil {
		s.MinProps = *f.MinProperties
	}
	switch {
	case f.NotPattern != "":
		s.Not = openapi3.NewSchemaRef("", &openapi3.Schema{Pattern: f.NotPattern})
	case len(f.NotEqual) > 0:
		s.Not = openapi3.NewSchemaRef("", &openapi3.Schema{Enum: f.NotEqual})
	}
	return s
}

// branches renders one oneOf entry per discriminator value, pinning the value
// and requiring the conditional fields that value brings.
func (f *Field) branches() openapi3.SchemaRefs {
	var disc *Field
	for _, c := range f.Fields {
		if c.Name == f.Discriminator {
			disc = c
		}
	}
	if disc == nil {
		return nil
	}
	values := disc.Enum
	if disc.Kind == KindBool {
		values = []any{true, false}
	}
	var out openapi3.SchemaRefs
	for _, v := range values {
		b := &openapi3.Schema{Properties: openapi3.Schemas{
			f.Discriminator: openapi3.NewSchemaRef("", &openapi3.Schema{Enum: []any{v}}),
		}}
		for _, c := range f.Fields {
			if c.Condition != nil && !c.Optional && containsValue(c.Condition.Values, v) {
				b.Required = append(b.Required, c.Name)
			}
		}
		out = append(out, openapi3.NewSchemaRef("", b))
	}
	return out
}

// UIParameters renders an object's fields as the default VelaUX form, in the
// order the template declares them.
func (f *Field) UIParameters() uischema.UISchema {
	var out uischema.UISchema
	for i, c := range f.formFields() {
		p := c.uiParameter()
		p.Sort = uint(100 + i)
		out = append(out, p)
	}
	return out
}

func (f *Field) uiParameter() *uischema.UIParameter {
	p := &uischema.UIParameter{
		JSONKey:     f.Name,
		Label:       stringtools.Capitalize(f.Name),
		Description: f.Description,
		Validate: &uischema.Validate{
			Required:     !f.Optional,
			DefaultValue: f.Default,
			Min:          f.Min,
			Max:          f.Max,
			MaxLength:    f.MaxLength,
			Pattern:      f.Pattern,
			Immutable:    f.Immutable,
		},
	}
	if f.MinLength != nil {
		p.Validate.MinLength = *f.MinLength
	}
	for _, e := range f.Enum {
		p.Validate.Options = append(p.Validate.Options, uischema.Option{Label: optionLabel(e), Value: e})
	}
	if c := f.Condition; c != nil {
		cond := uischema.Condition{JSONKey: c.Field, Value: c.Values[0]}
		if len(c.Values) > 1 {
			cond.Op, cond.Value = "in", c.Values
		}
		p.Conditions = []uischema.Condition{cond}
	}

	switch f.Kind {
	case KindString, KindBytes:
		p.UIType = "Input"
		if len(f.Enum) > 1 {
			p.UIType = "Select"
		}
	case KindInt, KindNumber:
		p.UIType = "Number"
		if len(f.Enum) > 1 {
			p.UIType = "Select"
		}
	case KindBool:
		p.UIType = "Switch"
	case KindArray:
		p.UIType = "Structs"
		if f.Items != nil {
			switch f.Items.Kind {
			case KindString:
				p.UIType = "Strings"
			case KindInt, KindNumber:
				p.UIType = "Numbers"
			case KindObject:
				p.SubParameters = f.Items.UIParameters()
			}
		}
	case KindMap:
		p.UIType = "KV"
		if f.Values != nil {
			additional := true
			p.Additional = &additional
			p.AdditionalParameter = f.Values.uiParameter()
			if f.Values.Kind == KindObject {
				p.SubParameters = f.Values.UIParameters()
			}
		}
	case KindObject:
		p.UIType = "Group"
		if len(f.Fields) == 0 {
			p.UIType = "KV"
		}
		p.SubParameters = f.UIParameters()
	case KindOneOf:
		// No widget chooses between shapes, so show the union, as VelaUX
		// does for the OpenAPI it derives from.
		p.UIType = "Input"
		if sub := f.UIParameters(); len(sub) > 0 {
			p.UIType = "Group"
			p.SubParameters = sub
		}
	default:
		p.UIType = "Input"
	}
	return p
}

func optionLabel(v any) string {
	if s, ok := v.(string); ok {
		return stringtools.Capitalize(s)
	}
	return fmt.Sprint(v)
}

// formFields are the fields a form shows for a value: an object's own, or
// for a choice between objects, the union of theirs, since no widget chooses
// between shapes. A map shows none; its values are entered as KV.
func (f *Field) formFields() []*Field {
	switch f.Kind {
	case KindObject:
		return f.Fields
	case KindOneOf:
		var out []*Field
		seen := map[string]bool{}
		for _, v := range f.Variants {
			for _, c := range v.formFields() {
				if !seen[c.Name] {
					seen[c.Name] = true
					out = append(out, c)
				}
			}
		}
		return out
	}
	return nil
}

func (f *Field) allObjects() bool {
	for _, v := range f.Variants {
		if v.Kind != KindObject {
			return false
		}
	}
	return len(f.Variants) > 0
}
