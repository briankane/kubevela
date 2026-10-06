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
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"

	"github.com/oam-dev/kubevela/pkg/cue/process"
)

// Definition types as the header spells them.
const (
	componentType    = "component"
	traitType        = "trait"
	policyType       = "policy"
	workflowStepType = "workflow-step"
	sourceType       = "source"
)

// ContextField is a field the controller sets on context when it renders a
// template.
type ContextField struct {
	Name string
	// Type is the field's CUE type. A trailing "?" in Name marks it optional.
	Type string
	Doc  string
}

// workloadContext is what process.NewContext sets for every component and trait.
var workloadContext = []ContextField{
	{process.ContextName, "string", "Name of the component being rendered."},
	{process.ContextNamespace, "string", "Namespace the Application deploys to."},
	{process.ContextAppName, "string", "Name of the Application."},
	{process.ContextAppRevision, "string", "Name of the current ApplicationRevision."},
	{process.ContextAppRevisionNum, "int", "Number of the current ApplicationRevision."},
	{process.ContextCompRevisionName, "string", "Name of the component's revision."},
	{process.ContextWorkflowName, "string", "Name of the Application's workflow."},
	{process.ContextPublishVersion, "string", "The app.oam.dev/publishVersion annotation of the Application."},
	{process.ContextComponents, "_", "Every component of the Application."},
	{process.ContextAppLabels, "[string]: string", "Labels of the Application."},
	{process.ContextAppAnnotations, "[string]: string", "Annotations of the Application."},
	{process.ContextReplicaKey, "string", "Key of the replica being rendered by a replication policy."},
	{process.ContextCluster, "string", "Cluster the component is dispatched to."},
	{process.ContextClusterVersion, "{major: string, minor: int, gitVersion: string, platform: string}", "Kubernetes version of that cluster."},
	{process.ContextComponentName, "string", "Name of the component, whichever definition is being rendered."},
	{process.ContextComponentType, "string", "Type of the component."},
	{process.ContextAppSources, "[string]: _", "Values of the Application's source bindings."},
	{process.ContextAppSourceTypes, "[string]: string", "Definition type of each source binding."},
	{process.ContextAppSourceTemplates, "[string]: string", "CUE template of each source definition type."},
	{process.ContextAppSourceSensitivePaths, "[string]: [...string]", "Platform-sensitive paths of each source definition type."},
	{process.ContextAppSourceCacheStore + "?", "_", "Cache the sources are read through."},
	{"config?", "[...{name: string, value: string}]", "Configuration injected by the platform."},
	{"custom?", "{...}", "Data a policy added for the components it applies to."},
}

// traitContext is what a trait sees on top of workloadContext.
var traitContext = []ContextField{
	{process.ContextTraitType, "string", "Type of the trait being rendered."},
	{process.OutputFieldName, "{...}", "The component's rendered output."},
	{process.OutputsFieldName, "[string]: {...}", "The component's rendered outputs, by name."},
}

// componentContext is what a component sees on top of workloadContext.
var componentContext = []ContextField{
	{process.OutputFieldName + "?", "{...}", "The component's own rendered output, once rendered."},
	{process.OutputsFieldName + "?", "[string]: {...}", "The component's own rendered outputs, once rendered."},
}

// ContextFields lists the context fields of a definition type, or nil for a
// type whose context is not modelled, which is then left open.
func ContextFields(defType string) []ContextField {
	switch defType {
	case componentType:
		return append(append([]ContextField{}, workloadContext...), componentContext...)
	case traitType:
		return append(append([]ContextField{}, workloadContext...), traitContext...)
	}
	return nil
}

// contextField is the context declaration injected into a template: closed
// for a modelled type, so a misspelt key is an error, and open otherwise.
func contextField(defType string) *ast.Field {
	fields := ContextFields(defType)
	body := "{...}"
	if fields != nil {
		var b strings.Builder
		for _, f := range fields {
			fmt.Fprintf(&b, "%s: %s\n", f.Name, f.Type)
		}
		body = "close({\n" + b.String() + "})"
	}
	return mustField("context: " + body)
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
