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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// resourceFindings are what checking a template body of a type finds of
// resources, as "line severity: message".
func resourceFindings(defType, body string) []string {
	attrs := "\tattributes: workload: type: \"autodetects.core.oam.dev\"\n"
	if defType == traitType {
		attrs = "\tattributes: appliesToWorkloads: [\"deployments.apps\"]\n"
	}
	src := "\"d\": {\n\ttype: \"" + defType + "\"\n" + attrs + "}\ntemplate: {\n" + body + "\tparameter: {kind: *\"ConfigMap\" | string}\n}\n"
	var out []string
	for _, d := range AnalyzeWith("d.cue", []byte(src), Options{}).Diagnostics {
		if strings.Contains(d.Message, "Kubernetes object") {
			sev := map[Severity]string{SeverityError: "error", SeverityWarning: "warning", SeverityInfo: "info"}[d.Severity]
			out = append(out, strconv.Itoa(d.Range.Start.Line)+" "+sev+": "+d.Message)
		}
	}
	return out
}

func TestOutputsAreResources(t *testing.T) {
	flagged := map[string]struct {
		defType, body string
		want          []string
	}{
		"an empty output":            {componentType, "\toutput: {}\n", []string{"6 error", "apiVersion", "kind"}},
		"an output with no kind":     {componentType, "\toutput: {apiVersion: \"v1\", metadata: name: \"x\"}\n", []string{"6 error", "output", "kind"}},
		"an outputs object, no kind": {componentType, "\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\toutputs: svc: {apiVersion: \"v1\"}\n", []string{"7 error", "outputs.svc", "kind"}},
		"a trait's outputs object":   {traitType, "\toutputs: cm: {metadata: name: \"x\"}\n", []string{"6 error", "outputs.cm", "apiVersion"}},
		"a trait's outputs, empty":   {traitType, "\toutputs: {}\n", []string{"6 warning", "nothing to render"}},
	}
	for name, c := range flagged {
		got := resourceFindings(c.defType, c.body)
		if assert.Len(t, got, 1, "%s: %v", name, got) {
			for _, w := range c.want {
				assert.Contains(t, got[0], w, name)
			}
		}
	}
	clean := map[string]struct{ defType, body string }{
		"a resource":                {componentType, "\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n"},
		"a kind from a parameter":   {componentType, "\toutput: {apiVersion: \"v1\", kind: parameter.kind}\n"},
		"declared in parts":         {componentType, "\toutput: apiVersion: \"v1\"\n\toutput: kind: \"ConfigMap\"\n"},
		"made by a condition":       {componentType, "\toutput: {if parameter.kind == \"x\" {apiVersion: \"v1\", kind: \"x\"}}\n"},
		"embedded":                  {componentType, "\t_obj: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\toutput: {_obj}\n"},
		"outputs made by a loop":    {traitType, "\toutputs: {for k in [\"a\"] {(k): {apiVersion: \"v1\", kind: \"ConfigMap\"}}}\n"},
		"a trait that only patches": {traitType, "\tpatch: metadata: labels: a: \"b\"\n"},
	}
	for name, c := range clean {
		assert.Empty(t, resourceFindings(c.defType, c.body), name)
	}
}
