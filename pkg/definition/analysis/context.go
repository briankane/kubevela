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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/format"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

// Definition types as the header spells them.
const (
	componentType    = "component"
	traitType        = "trait"
	policyType       = "policy"
	workflowStepType = "workflow-step"
	sourceType       = "source"
	workloadType     = "workload"
)

// contextLabel is the field a template reads its context from.
const contextLabel = "context"

// templateContexts are propexpr's registry entries for what each definition
// type's template reads: the source of truth for which context fields exist
// there, their types, and their docs.
var templateContexts = map[string]propexpr.ContextSchema{
	componentType:    propexpr.ComponentContext,
	traitType:        propexpr.TraitContext,
	workflowStepType: propexpr.WorkflowStepTemplateContext,
	policyType:       propexpr.RenderedPolicyContext,
}

// contextOrder is the order types are named in a message.
var contextOrder = []string{componentType, traitType, workflowStepType, policyType}

// ContextField is a field on context when a template renders.
type ContextField struct {
	Name string
	// Type is the field's CUE type.
	Type string
	Doc  string
	// Required is false for a field the render may leave out.
	Required bool
	// Hidden is set for a field the registry excludes: readable, guarded,
	// but not offered.
	Hidden bool
}

// ContextFields lists the context fields a definition type's template can
// read, sorted, or nil for a type whose context is not modelled.
//
// They are the registry's fields for the type, plus what the render context
// carries that the registry offers no expression: a template runs against the
// whole render context, so it may read those too, guarded. output and outputs
// are the rendered objects: a trait always has its component's, other types
// only once rendered.
func ContextFields(defType string) []ContextField {
	schema, ok := templateContexts[defType]
	if !ok {
		return nil
	}
	var fields []ContextField
	for _, name := range schema.ReadableFields() {
		v, _ := schema.FieldValue(name)
		fields = append(fields, ContextField{Name: name, Type: registryType(v), Doc: usageOf(v), Required: true})
	}
	for name, reason := range propexpr.ExcludedFields() {
		switch {
		case schema.Offers(name), name == parameterLabel:
			continue
		case name == process.OutputFieldName:
			fields = append(fields, ContextField{Name: name, Type: "{...}", Doc: "The rendered output.", Required: defType == traitType})
		case name == process.OutputsFieldName:
			fields = append(fields, ContextField{Name: name, Type: "[string]: {...}", Doc: "The rendered outputs, by name.", Required: defType == traitType})
		default:
			fields = append(fields, ContextField{Name: name, Type: "_", Doc: reason, Hidden: true})
		}
	}
	// BaseTemplate declares config, which the registry does not know.
	if !schema.Offers("config") {
		fields = append(fields, ContextField{Name: "config", Type: "[...{name: string, value: string}]", Doc: "Configuration the platform injects."})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

// registryType is a registry field's type as CUE source.
func registryType(v cue.Value) string {
	b, err := format.Node(v.Syntax(cue.Docs(false)))
	if err != nil {
		return "_"
	}
	return string(b)
}

// usageOf is the +usage text of a registry field's doc comment.
func usageOf(v cue.Value) string {
	var lines []string
	for _, cg := range v.Doc() {
		for _, line := range strings.Split(cg.Text(), "\n") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "+usage=")))
		}
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// contextDefinition names the context's type: a definition, so every struct
// in it is closed and a misspelt key is an error at any depth.
const contextDefinition = "#velaContext"

// contextField is the context declaration injected into a template: closed
// for a modelled type, so a misspelt key is an error, and open otherwise.
func contextField(defType string) *ast.Field {
	if ContextFields(defType) == nil {
		return mustField("context: {...}")
	}
	return mustField("context: " + contextDefinition)
}

// contextType declares contextDefinition for a modelled type.
func contextType(defType string) []ast.Decl {
	fields := ContextFields(defType)
	if fields == nil {
		return nil
	}
	var b strings.Builder
	for _, f := range fields {
		mark := "?"
		if f.Required {
			mark = ""
		}
		fmt.Fprintf(&b, "%s%s: %s\n", strconv.Quote(f.Name), mark, f.Type)
	}
	return []ast.Decl{mustField(contextDefinition + ": {\n" + b.String() + "}")}
}

// explainContextField says which definition types can read a context field a
// template of this type cannot.
func explainContextField(defType, field string) (string, bool) {
	here, ok := templateContexts[defType]
	if !ok || here.Offers(field) {
		return "", false
	}
	var offered []string
	for _, t := range contextOrder {
		if t != defType && templateContexts[t].Offers(field) {
			offered = append(offered, templateContexts[t].Plural())
		}
	}
	if len(offered) == 0 {
		return "", false
	}
	return fmt.Sprintf("context.%s is available to %s, not %s", field, orList(offered), here.Plural()), true
}

// closedParameterPath is a closed copy of the template's parameter, so a
// reference to a field it does not declare can be told apart from one that is
// merely unset.
const closedParameterPath = "#velaAnalysisParameter"

func closedParameterField() *ast.Field {
	return mustField(closedParameterPath + ": " + templateLabel + "." + parameterLabel)
}

func mustField(src string) *ast.Field {
	f, err := parser.ParseFile("", src)
	if err != nil {
		panic(err)
	}
	return f.Decls[0].(*ast.Field)
}
