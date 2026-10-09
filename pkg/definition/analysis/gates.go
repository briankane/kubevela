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

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
)

// ControllerGates are the feature gates of the cluster's KubeVela
// controller, as far as they are known.
type ControllerGates struct {
	// Context is the kubeconfig context the controller runs on, and Version
	// its image's version, for messages.
	Context string
	Version string
	// On is each gate whose value is known: set in the controller's args, or
	// its default as the controller's --help gives it.
	On map[string]bool
	// Missing are the gates the controller's --help does not list: its
	// version has no such gate.
	Missing map[string]bool
}

// FixCommand is a command a quick fix runs instead of editing the file.
type FixCommand struct {
	Name      string
	Arguments []interface{}
}

// The command that opens the Controller Feature Gates panel, given a gate to change.
const controllerPanelCommand = "kubevela.controller.open"

// check is a warning at r when gate is off, or missing from the
// controller, for a file using feature; effect is what the controller does
// with it while the gate is off. Nothing is said of a gate not known.
func (g *ControllerGates) check(gate, feature, effect string, r Range) []Diagnostic {
	if g == nil {
		return nil
	}
	if g.Missing[gate] {
		where := g.Context
		if g.Version != "" {
			where += " (" + g.Version + ")"
		}
		return []Diagnostic{{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("the controller on %s has no %s, so it does not support %s", where, gate, feature)}}
	}
	if on, known := g.On[gate]; !known || on {
		return nil
	}
	return []Diagnostic{{
		Range:    r,
		Severity: SeverityWarning,
		Message:  fmt.Sprintf("%s is off on %s: %s", gate, g.Context, effect),
		Fixes: []Fix{{
			Title:   "Turn on " + gate + " in the controller's feature gates",
			Command: &FixCommand{Name: controllerPanelCommand, Arguments: []interface{}{map[string]interface{}{"gate": gate, "value": true}}},
		}},
	}}
}

// headerField is the header field at path, if written.
func (d *document) headerField(path ...string) (*ast.Field, bool) {
	if len(d.headers) == 0 {
		return nil, false
	}
	f := d.headers[0]
	for _, p := range path {
		child, ok := fieldIn(f, p)
		if !ok {
			return nil, false
		}
		f = child
	}
	return f, true
}

// checkDefinitionGates warns of a definition using a feature its cluster's
// controller has off.
func (d *document) checkDefinitionGates() []Diagnostic {
	g := d.opts.Gates
	if g == nil || len(d.headers) == 0 {
		return nil
	}
	label := func(f *ast.Field) Range { return span(f.Label.Pos(), f.Label.End()) }
	var diags []Diagnostic
	if d.parentName() != "" {
		f, ok := d.headerField("extends")
		if !ok {
			f, ok = d.headerField("attributes", "extends")
		}
		if ok {
			diags = append(diags, g.check("EnableDefinitionInheritance", "extends", "the controller refuses a definition that sets extends", label(f))...)
		}
	}
	if d.typ == sourceType {
		diags = append(diags, g.check("EnableCelExpressions", "SourceDefinitions", "the controller reads no SourceDefinition", label(d.headers[0]))...)
	}
	if d.typ == policyType {
		if f, ok := d.headerField("attributes", "scope"); ok && d.headerString("attributes", "scope") == "Application" {
			diags = append(diags, g.check("EnableApplicationScopedPolicies", "policies with scope: Application", "the controller does not apply a policy with scope: Application", label(f))...)
		}
		if f, ok := d.headerField("attributes", "global"); ok && d.headerBool("attributes", "global") {
			diags = append(diags, g.check("EnableGlobalPolicies", "policies with global: true", "the controller does not discover a policy with global: true", label(f))...)
		}
	}
	return diags
}

// celOptIn is the annotation an Application asks for expressions with,
// while RequireCelExpressionOptIn is on.
const celOptIn = "app.oam.dev/cel-expressions"

// checkApplicationGates warns of an Application using a feature its
// cluster's controller has off: $( ) expressions, sources, and addon
// components.
func (d *document) checkApplicationGates(app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	g := d.opts.Gates
	if g == nil {
		return nil
	}
	var diags []Diagnostic
	if expr := firstExpression(fields); expr != nil {
		r := d.scalarRange(expr.Value.Pos())
		cel := g.check("EnableCelExpressions", "$( ) expressions", "the controller does not read $( ) expressions", r)
		diags = append(diags, cel...)
		optedIn, _ := app.LookupPath(cue.MakePath(cue.Str("metadata"), cue.Str("annotations"), cue.Str(celOptIn))).String()
		if len(cel) == 0 && g.On["EnableCelExpressions"] && g.On["RequireCelExpressionOptIn"] && optedIn != "true" {
			diags = append(diags, Diagnostic{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("RequireCelExpressionOptIn is on at %s: expressions are read only in an Application annotated %s: \"true\"", g.Context, celOptIn)})
		}
	}
	if f, ok := fields["spec.sources.0.type"]; ok {
		diags = append(diags, g.check("EnableCelExpressions", "SourceDefinitions", "the controller reads no SourceDefinition", d.scalarRange(f.Value.Pos()))...)
	}
	for i := 0; ; i++ {
		f, ok := fields[fmt.Sprintf("spec.components.%d.type", i)]
		if !ok {
			break
		}
		if lit, ok := f.Value.(*ast.BasicLit); ok && strings.Trim(lit.Value, `"'`) == "addon" {
			diags = append(diags, g.check("EnableAddonComponent", "type: addon components", "an Application with type: addon fails at render", d.scalarRange(f.Value.Pos()))...)
		}
	}
	return diags
}

// firstExpression is the first field, by position, whose value holds a
// $( ) expression.
func firstExpression(fields map[string]*ast.Field) *ast.Field {
	var first *ast.Field
	for _, f := range fields {
		lit, ok := f.Value.(*ast.BasicLit)
		if !ok || !strings.Contains(lit.Value, "$(") {
			continue
		}
		if first == nil || f.Pos().Offset() < first.Pos().Offset() {
			first = f
		}
	}
	return first
}
