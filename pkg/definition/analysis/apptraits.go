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
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/parser"
	k8slabels "k8s.io/apimachinery/pkg/labels"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// attachedTrait is a trait attached to a component: what its header says
// of where it attaches.
type attachedTrait struct {
	name          string
	appliesTo     []string
	conflictsWith []string
	labels        map[string]string
}

// definitionHeaderOf parses a definition's CUE for its header.
func definitionHeaderOf(def AppDefinition) (*document, bool) {
	f, err := parser.ParseFile(def.Name+".cue", def.CUE, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	d, ok := newDocument(def.Name+".cue", []byte(def.CUE), f)
	return d, ok && len(d.headers) > 0
}

// headerStrings is the list of strings at a path in the header.
func (d *document) headerStrings(path ...string) []string {
	f := d.headers[0]
	for _, p := range path {
		child, ok := fieldIn(f, p)
		if !ok {
			return nil
		}
		f = child
	}
	list, ok := f.Value.(*ast.ListLit)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list.Elts {
		if lit, ok := e.(*ast.BasicLit); ok {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// headerLabels is the header's labels.
func (d *document) headerLabels() map[string]string {
	out := map[string]string{}
	f, ok := fieldIn(d.headers[0], "labels")
	if !ok {
		return out
	}
	s, ok := f.Value.(*ast.StructLit)
	if !ok {
		return out
	}
	for _, e := range s.Elts {
		if fd, ok := e.(*ast.Field); ok {
			if lit, ok := fd.Value.(*ast.BasicLit); ok {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					out[labelName(fd.Label)] = v
				}
			}
		}
	}
	return out
}

// workloadNames are the names a trait's appliesToWorkloads can give a
// component's workload by: its type, its workload type, and its workload's
// resource, as deployments.apps, from its workload definition or, for one
// detected, the kind its output declares.
func workloadNames(def AppDefinition) ([]string, string, bool) {
	d, ok := definitionHeaderOf(def)
	if !ok {
		return nil, "", false
	}
	names := []string{def.Name}
	if t := d.headerString("attributes", "workload", "type"); t != "" && t != autodetectWorkload {
		names = append(names, t)
	}
	var gvk kubeschema.GVK
	api, kind := d.headerString("attributes", "workload", "definition", "apiVersion"), d.headerString("attributes", "workload", "definition", "kind")
	switch {
	case api != "" && kind != "":
		gvk = kubeschema.ParseGVK(api, kind)
	default:
		if g, ok := d.outputKind(); ok {
			gvk = g
		}
	}
	described := def.Name
	if gvk.Kind != "" {
		resource := resourceName(gvk)
		names = append(names, resource)
		described = fmt.Sprintf("%s (%s)", describe(gvk), resource)
	}
	return names, described, true
}

// resourceName is a kind's resource, as appliesToWorkloads names it:
// plural, lower case, and its group after a dot.
func resourceName(gvk kubeschema.GVK) string {
	k := strings.ToLower(gvk.Kind)
	switch {
	case strings.HasSuffix(k, "s"), strings.HasSuffix(k, "x"), strings.HasSuffix(k, "z"), strings.HasSuffix(k, "ch"), strings.HasSuffix(k, "sh"):
		k += "es"
	case strings.HasSuffix(k, "y") && len(k) > 1 && !strings.ContainsAny(k[len(k)-2:len(k)-1], "aeiou"):
		k = k[:len(k)-1] + "ies"
	default:
		k += "s"
	}
	if gvk.Group != "" {
		k += "." + gvk.Group
	}
	return k
}

// conflictRuleMatches reports whether a conflictsWith rule matches a trait,
// as KubeVela's admission webhook matches it: "*", its name, or a label
// selector over its labels.
func conflictRuleMatches(rule string, t attachedTrait) bool {
	switch {
	case rule == "*", rule == t.name:
		return true
	case strings.HasPrefix(rule, "labelSelector:"):
		sel, err := k8slabels.Parse(strings.TrimPrefix(rule, "labelSelector:"))
		return err == nil && sel.Matches(k8slabels.Set(t.labels))
	}
	return false
}

// conflicts reports whether either trait's conflictsWith matches the other.
func conflicts(a, b attachedTrait) bool {
	for _, r := range a.conflictsWith {
		if conflictRuleMatches(r, b) {
			return true
		}
	}
	for _, r := range b.conflictsWith {
		if conflictRuleMatches(r, a) {
			return true
		}
	}
	return false
}

// checkTraits checks the traits each component attaches: two that conflict
// are an error, as KubeVela's admission webhook refuses them; one whose
// appliesToWorkloads does not name the component's workload is a warning,
// as KubeVela attaches it all the same.
func (d *document) checkTraits(app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	comps, err := app.LookupPath(cue.ParsePath("spec.components")).List()
	if err != nil {
		return nil
	}
	var diags []Diagnostic
	for i := 0; comps.Next(); i++ {
		comp := comps.Value()
		compName, _ := comp.LookupPath(cue.ParsePath("name")).String()
		compType, _ := comp.LookupPath(cue.ParsePath("type")).String()
		traits, err := comp.LookupPath(cue.ParsePath("traits")).List()
		if err != nil {
			continue
		}
		var workloads []string
		var workloadDesc string
		knownWorkload := false
		if def, ok := d.opts.Applications.Lookup(componentType, compType); ok {
			workloads, workloadDesc, knownWorkload = workloadNames(def)
		}
		var attached []attachedTrait
		for j := 0; traits.Next(); j++ {
			typeName, _ := traits.Value().LookupPath(cue.ParsePath("type")).String()
			def, ok := d.opts.Applications.Lookup(traitType, typeName)
			if !ok {
				continue
			}
			td, ok := definitionHeaderOf(def)
			if !ok {
				continue
			}
			th := attachedTrait{name: typeName, appliesTo: td.headerStrings("attributes", "appliesToWorkloads"), conflictsWith: td.headerStrings("attributes", "conflictsWith"), labels: td.headerLabels()}
			at := fmt.Sprintf("spec.components.%d.traits.%d.type", i, j)
			r, placed := fieldRange(fields, at)
			for _, other := range attached {
				if conflicts(other, th) && placed {
					diags = append(diags, Diagnostic{Range: r, Severity: SeverityError, Message: fmt.Sprintf("trait %q conflicts with trait %q on component %q: KubeVela refuses the Application", other.name, th.name, compName)})
				}
			}
			attached = append(attached, th)
			if knownWorkload && placed && len(th.appliesTo) > 0 && !appliesTo(th.appliesTo, workloads) {
				diags = append(diags, Diagnostic{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("%s applies to %s, not %s's workload, %s: KubeVela attaches it all the same, but it was written for other workloads", th.name, strings.Join(th.appliesTo, ", "), compName, workloadDesc)})
			}
		}
	}
	return diags
}

// appliesTo reports whether a trait's appliesToWorkloads names a workload.
func appliesTo(rules, workloads []string) bool {
	for _, r := range rules {
		if r == "*" {
			return true
		}
		for _, w := range workloads {
			if r == w {
				return true
			}
		}
	}
	return false
}
