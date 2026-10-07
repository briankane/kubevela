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
	"github.com/stretchr/testify/require"
)

// statusDef's lines and columns are counted on below: healthPolicy's CUE
// starts on line 7, customStatus's on line 12, at column 4.
const statusDef = `"web": {
	type: "component"
	attributes: {
		workload: type: "autodetects.core.oam.dev"
		status: {
			healthPolicy: #"""
				ready: *context.output.status.readyReplicas | 0
				isHealth: ready == context.output.spec.replicas
				"""#
			customStatus: {
				message: "\(*context.output.status.readyReplicas | 0)/\(parameter.replicas) ready"
			}
		}
	}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: replicas: parameter.replicas
	}
	parameter: {
		// +usage=How many to run
		replicas: *1 | int
	}
}
`

func statusErrors(t *testing.T, src string) []string {
	t.Helper()
	var out []string
	for _, d := range AnalyzeWith("def.cue", []byte(src), Options{}).Diagnostics {
		if d.Severity == SeverityError {
			out = append(out, strconv.Itoa(d.Range.Start.Line)+":"+strconv.Itoa(d.Range.Start.Column)+" "+d.Message)
		}
	}
	return out
}

func TestStatusFields(t *testing.T) {
	assert.Empty(t, statusErrors(t, statusDef), "a valid status")
	assert.Empty(t, statusErrors(t, strings.Replace(statusDef, "isHealth: ready == context.output.spec.replicas", "isHealth: ready == context.parameter.replicas", 1)), "the controller sets context.parameter too")

	cases := map[string]struct {
		edit func(string) string
		want []string
	}{
		"a misspelt field of the live object, in a string": {
			edit: func(s string) string {
				return strings.Replace(s, "ready: *context.output.status.readyReplicas", "ready: *context.output.status.readyRepicas", 1)
			},
			want: []string{"7:", "readyRepicas"},
		},
		"a misspelt context field": {
			edit: func(s string) string {
				return strings.Replace(s, "context.output.spec.replicas", "context.outptu.spec.replicas", 1)
			},
			want: []string{"8:", "outptu"},
		},
		"a misspelt parameter, in native CUE": {
			edit: func(s string) string {
				return strings.Replace(s, "parameter.replicas) ready", "parameter.replica) ready", 1)
			},
			want: []string{"11:", "replica"},
		},
		"a misspelt parameter read through context": {
			edit: func(s string) string {
				return strings.Replace(s, "isHealth: ready == context.output.spec.replicas", "isHealth: ready == context.parameter.replica", 1)
			},
			want: []string{"8:", "replica"},
		},
		"a health policy that sets no isHealth": {
			edit: func(s string) string { return strings.Replace(s, "isHealth: ready ==", "healthy: ready ==", 1) },
			want: []string{"isHealth"},
		},
		"a message that is not a string": {
			edit: func(s string) string {
				return strings.Replace(s, `message: "\(*context.output.status.readyReplicas | 0)/\(parameter.replicas) ready"`, "message: 42", 1)
			},
			want: []string{"11:", "message"},
		},
		"a syntax error in a string": {
			edit: func(s string) string { return strings.Replace(s, "isHealth: ready ==", "isHealth: ready ===", 1) },
			want: []string{"8:"},
		},
		"a KubeVela package": {
			edit: func(s string) string {
				return strings.Replace(s, "\t\t\t\tready: *context", "\t\t\t\timport \"vela/kube\"\n\t\t\t\tready: *context", 1)
			},
			want: []string{"vela/kube"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := statusErrors(t, tc.edit(statusDef))
			require.NotEmpty(t, got, "want %v", tc.want)
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}

// A position in the string is mapped back to its column in the file.
func TestStatusFieldPositions(t *testing.T) {
	src := strings.Replace(statusDef, "ready: *context.output.status.readyReplicas", "ready: *context.output.status.readyRepicas", 1)
	got := statusErrors(t, src)
	require.NotEmpty(t, got)
	line := strings.Split(src, "\n")[6]
	assert.Equal(t, "7:"+strconv.Itoa(strings.Index(line, "readyRepicas")+1), strings.Fields(got[0])[0])
}

func TestCompleteInStatusFields(t *testing.T) {
	complete := func(marker string) []string {
		cursor := strings.Index(statusDef, marker) + len(marker)
		got, ok := CompleteStatusField("def.cue", statusDef, cursor, Options{})
		require.True(t, ok, "in a status field")
		var labels []string
		for _, c := range got {
			labels = append(labels, c.Label)
		}
		return labels
	}
	assert.Contains(t, complete("context.output.status.r"), "readyReplicas", "the live object's status, from its kind")
	assert.Contains(t, complete("context.o"), "output")
	assert.Contains(t, complete("parameter.r"), "replicas")

	_, ok := CompleteStatusField("def.cue", statusDef, strings.Index(statusDef, "spec: replicas"), Options{})
	assert.False(t, ok, "the template is not a status field")
}

func TestHoverInStatusFields(t *testing.T) {
	at := strings.Index(statusDef, "readyReplicas | 0\n") + 3
	h, ok := HoverStatusField("def.cue", statusDef, at, Options{})
	require.True(t, ok)
	assert.Contains(t, h, "readyReplicas")
	assert.Contains(t, h, "int")
}

func TestStatusReturnTypes(t *testing.T) {
	withDetails := func(details string) string {
		return strings.Replace(statusDef, "\t\t\tcustomStatus: {", "\t\t\tdetails: #\"\"\"\n"+details+"\t\t\t\t\"\"\"#\n\t\t\tcustomStatus: {", 1)
	}
	findings := func(src string) []Diagnostic {
		var out []Diagnostic
		for _, d := range AnalyzeWith("def.cue", []byte(src), Options{}).Diagnostics {
			if d.Severity != SeverityInfo && (strings.Contains(d.Message, "details") || strings.Contains(d.Message, "isHealth") || strings.Contains(d.Message, "message")) {
				out = append(out, d)
			}
		}
		return out
	}
	assert.Empty(t, findings(withDetails("\t\t\t\tready: \"\\(*context.output.status.readyReplicas | 0)\"\n\t\t\t\t$hidden: 1\n\t\t\t\tlocal: 2 @local()\n")), "valid details")

	got := findings(withDetails("\t\t\t\tthisLabelIsFarTooLongForKubeVelaToShowIt: \"x\"\n"))
	require.Len(t, got, 1)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Contains(t, got[0].Message, "32")

	got = findings(withDetails("\t\t\t\tparameter: \"x\"\n"))
	require.Len(t, got, 1, "a detail named parameter unifies with the parameter, and conflicts")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Contains(t, got[0].Message, "conflicting")

	got = findings(withDetails("\t\t\t\tcount: int\n"))
	require.Len(t, got, 1)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Contains(t, got[0].Message, "_|_")

	got = findings(strings.Replace(statusDef, "isHealth: ready == context.output.spec.replicas", "isHealth: bool", 1))
	require.Len(t, got, 1)
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Contains(t, got[0].Message, "a value")
}

func TestDeclarationInStatusFields(t *testing.T) {
	// ready, declared on line 7, read on line 8.
	at := strings.Index(statusDef, "isHealth: ready") + len("isHealth: r")
	loc, ok := DeclarationInStatusField("def.cue", statusDef, at)
	require.True(t, ok)
	assert.Equal(t, 7, loc.Range.Start.Line)
	assert.Equal(t, strings.Index(strings.Split(statusDef, "\n")[6], "ready")+1, loc.Range.Start.Column)

	// parameter.replicas, to the template's declaration.
	at = strings.Index(statusDef, "parameter.replicas) ready") + len("parameter.re")
	loc, ok = DeclarationInStatusField("def.cue", statusDef, at)
	require.True(t, ok)
	assert.Equal(t, 24, loc.Range.Start.Line)
}

// Completing inside a comparison: the half-typed reference cannot
// evaluate there, and must not take the context with it.
func TestCompleteInStatusComparison(t *testing.T) {
	src := "\"web\": {\n\ttype: \"component\"\n\tattributes: {\n\t\tworkload: type: \"autodetects.core.oam.dev\"\n\t\tstatus: healthPolicy: #\"\"\"\n\t\t\tisHealth: context.output.status.readyReplicas == context.output.spec.replicas\n\t\t\t\"\"\"#\n\t}\n}\ntemplate: {\n\toutput: {apiVersion: \"apps/v1\", kind: \"Deployment\", spec: replicas: 1}\n\tparameter: {}\n}\n"
	got, ok := CompleteStatusField("def.cue", src, strings.Index(src, "status.readyReplicas")+len("status.re"), Options{})
	require.True(t, ok)
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	assert.Contains(t, labels, "readyReplicas")
}

// The render context's internal fields stay readable, guarded, but are not
// offered.
func TestContextCompletionHidesExcluded(t *testing.T) {
	labels := func(cs []Completion) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Label)
		}
		return out
	}
	at := strings.Index(statusDef, "context.output.spec.replicas") + len("context.")
	got, ok := CompleteStatusField("def.cue", statusDef[:at]+statusDef[at+len("output.spec.replicas"):], at, Options{})
	require.True(t, ok)
	inStatus := labels(got)
	inTemplate := labels(CompleteContextWith(statusDef, "\tname: context.", nil))
	for name, cs := range map[string][]string{"status": inStatus, "template": inTemplate} {
		assert.Contains(t, cs, "output", name)
		assert.Contains(t, cs, "appName", name)
		assert.NotContains(t, cs, "appSourceCacheStore", name)
		assert.NotContains(t, cs, "appComponents", name)
	}
	assert.Contains(t, inStatus, "status")

	guarded := strings.Replace(statusDef, "isHealth: ready == context.output.spec.replicas", "isHealth: ready == context.output.spec.replicas && context.appComponents != _|_", 1)
	assert.Empty(t, statusErrors(t, guarded), "an internal field is still readable")

	// appComponent is closest to appComponents, which is not offered, so the
	// fix names nothing internal.
	misspelt := strings.Replace(statusDef, "isHealth: ready == context.output.spec.replicas", "isHealth: ready == context.output.spec.replicas && context.appComponent != _|_", 1)
	for _, d := range AnalyzeWith("def.cue", []byte(misspelt), Options{}).Diagnostics {
		for _, f := range d.Fixes {
			assert.NotContains(t, f.Title, "appComponents")
		}
	}
}

// details written as CUE, not a string: completion works while the file
// does not parse, a reference half typed, and each value should be a
// string, which status.details holds.
func TestNativeDetails(t *testing.T) {
	native := func(body string) string {
		return strings.Replace(statusDef, "\t\t\tcustomStatus: {", "\t\t\tdetails: {\n"+body+"\t\t\t}\n\t\t\tcustomStatus: {", 1)
	}
	labels := func(src, marker string) []string {
		got, ok := CompleteStatusField("def.cue", src, strings.Index(src, marker)+len(marker), Options{})
		require.True(t, ok, "in details, at %q", marker)
		var out []string
		for _, c := range got {
			out = append(out, c.Label)
		}
		return out
	}
	assert.Contains(t, labels(native("\t\t\t\tready: context.output.status.\n"), "ready: context.output.status."), "readyReplicas")
	assert.Contains(t, labels(native("\t\t\t\tready: context.\n"), "ready: context."), "output")
	assert.Contains(t, labels(native("\t\t\t\tready: parameter.\n"), "ready: parameter."), "replicas")

	warnings := func(src string) []string {
		var out []string
		for _, d := range AnalyzeWith("def.cue", []byte(src), Options{}).Diagnostics {
			if d.Severity == SeverityWarning && strings.HasPrefix(d.Message, "details:") {
				out = append(out, strconv.Itoa(d.Range.Start.Line)+" "+d.Message)
			}
		}
		return out
	}
	got := warnings(native("\t\t\t\t\"new\": 1\n\t\t\t\tready: *context.output.status.readyReplicas | 0\n\t\t\t\tshown: \"\\(*context.output.status.readyReplicas | 0)\"\n\t\t\t\t$hidden: 1\n"))
	require.Len(t, got, 2, "%v", got)
	assert.Contains(t, got[0], "new is int")
	assert.Contains(t, got[0], "string")
	assert.Contains(t, got[1], "ready is int")

	assert.Empty(t, warnings(strings.Replace(statusDef, "\t\t\tcustomStatus: {", "\t\t\tdetails: #\"\"\"\n\t\t\t\tready: \"\\(*context.output.status.readyReplicas | 0)\"\n\t\t\t\t\"\"\"#\n\t\t\tcustomStatus: {", 1)), "a string")
}
