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

const parentWeb = `"web": {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: replicas: parameter.replicas
	}
	parameter: {
		// +usage=Image to run
		image: string
		// +usage=How many replicas
		replicas: *1 | int
		#Probe: path: string
		probe?: #Probe
		// +usage=Resources, each with a default
		resources: cpu: *"500m" | string
	}
}
`

func webLookup(name string) (string, []byte, bool) {
	if name == "web" {
		return "/defs/web.cue", []byte(parentWeb), true
	}
	return "", nil, false
}

func child(extends, template string) string {
	return "\"tenant-web\": {\n\ttype:    \"component\"\n\textends: \"" + extends + "\"\n}\ntemplate: {\n" + template + "}\n"
}

func TestExtends(t *testing.T) {
	opts := Options{Definitions: webLookup}
	cases := map[string]struct {
		src  string
		want []wantSev
	}{
		"properties the parent takes": {
			src: child("web", "\t$super: properties: {image: parameter.image, replicas: 2, probe: path: \"/healthz\"}\n\tparameter: image: string\n"),
		},
		"a version of the parent": {
			src: child("web@v3", "\t$super: properties: image: \"nginx\"\n"),
		},
		"no $super": {
			src:  child("web", "\tparameter: image: string\n"),
			want: []wantSev{{5, SeverityError, "a component that extends web must declare $super"}},
		},
		"a property the parent does not take": {
			src:  child("web", "\t$super: properties: {image: \"nginx\", imge: \"x\"}\n"),
			want: []wantSev{{6, SeverityError, "web takes no parameter imge"}},
		},
		"a property of the wrong type": {
			src:  child("web", "\t$super: properties: {image: \"nginx\", replicas: \"two\"}\n"),
			want: []wantSev{{6, SeverityError, "replicas"}},
		},
		"a nested property the parent does not take": {
			src:  child("web", "\t$super: properties: {image: \"nginx\", probe: paht: \"/\"}\n"),
			want: []wantSev{{6, SeverityError, "web takes no parameter probe.paht"}},
		},
		"a parameter the parent requires, left out": {
			src:  child("web", "\t$super: properties: replicas: 2\n"),
			want: []wantSev{{6, SeverityError, "web requires image"}},
		},
		"a parent found nowhere": {
			src:  child("elsewhere", "\t$super: properties: anything: 1\n"),
			want: []wantSev{{3, SeverityInfo, "elsewhere is not in the workspace, the cluster or KubeVela's own definitions"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := AnalyzeWith("tenant-web.cue", []byte(tc.src), opts)
			var diags []Diagnostic
			for _, d := range res.Diagnostics {
				if d.Severity != SeverityInfo || strings.Contains(d.Message, "workspace") {
					diags = append(diags, d)
				}
			}
			got := lines(diags)
			require.Len(t, diags, len(tc.want), strings.Join(got, "\n"))
			for i, w := range tc.want {
				assert.Equal(t, w.line, diags[i].Range.Start.Line, got[i])
				assert.Equal(t, w.sev, diags[i].Severity, got[i])
				assert.Contains(t, diags[i].Message, w.msg, got[i])
			}
		})
	}
}

// A parent outside the workspace is found as an Application's component
// type is: on the cluster, else among KubeVela's own definitions.
func TestExtendsAParentOutsideTheWorkspace(t *testing.T) {
	errorsOf := func(src string, opts Options) []string {
		var out []string
		for _, d := range AnalyzeWith("tenant-web.cue", []byte(src), opts).Diagnostics {
			if d.Severity == SeverityError || strings.Contains(d.Message, "workspace") {
				out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
			}
		}
		return out
	}
	assert.Equal(t, []string{"6: webservice requires image in $super.properties"}, errorsOf(child("webservice", "\t$super: properties: {}\n"), Options{}), "a built-in parent")
	assert.Empty(t, errorsOf(child("webservice", "\t$super: properties: image: \"nginx\"\n"), Options{}), "a built-in parent, given what it requires")

	cluster := LayeredDefinitions{{{Name: "cluster-web", Type: componentType, Source: SourceCluster, CUE: strings.Replace(parentWeb, `"web": {`, `"cluster-web": {`, 1)}}}
	assert.Equal(t, []string{"6: cluster-web requires image in $super.properties"}, errorsOf(child("cluster-web", "\t$super: properties: replicas: 2\n"), Options{Applications: cluster}), "a parent on the cluster")
}

// What a child passes its parent's required properties must be given:
// a value, and from a parameter the child requires too.
func TestSuperAlignsWithItsParent(t *testing.T) {
	opts := Options{Definitions: webLookup}
	find := func(src, msg string) *Diagnostic {
		for _, d := range AnalyzeWith("tenant-web.cue", []byte(src), opts).Diagnostics {
			if strings.Contains(d.Message, msg) {
				d := d
				return &d
			}
		}
		return nil
	}
	typ := find(child("web", "\t$super: properties: image: string\n"), "passes the type")
	require.NotNil(t, typ)
	assert.Equal(t, SeverityError, typ.Severity)
	assert.Contains(t, typ.Message, "image")

	src := child("web", "\t$super: properties: image: parameter.image\n\tparameter: {\n\t\timage?: string\n\t}\n")
	opt := find(src, "is optional here")
	require.NotNil(t, opt)
	assert.Equal(t, SeverityWarning, opt.Severity)
	assert.Contains(t, opt.Message, "web requires image")
	require.NotEmpty(t, opt.Fixes)
	assert.Contains(t, applyRangeEdits(src, opt.Fixes[0].Edits), "\t\timage: string\n")

	assert.Nil(t, find(child("web", "\t$super: properties: image: parameter.image\n\tparameter: image: string\n"), "is optional here"), "a required parameter")
	assert.Nil(t, find(child("web", "\t$super: properties: image: parameter.image\n\tparameter: image: *\"nginx\" | string\n"), "is optional here"), "a defaulted one")
}

// The scaffold of a child passes each property its parent requires through
// a parameter of the same type and usage, and checks clean.
func TestSuperPassThrough(t *testing.T) {
	tmpl, ok := SuperPassThrough("webservice", componentType, Options{})
	require.True(t, ok)
	assert.Contains(t, tmpl, "image: parameter.image")
	assert.Contains(t, tmpl, "// +usage=Which image would you like to use for your service\n")
	assert.NotContains(t, tmpl, "+short", "only the usage")
}

func TestCompleteSuperProperties(t *testing.T) {
	doc := child("web", "\t$super: properties: {\n\t\t\n\t}\n")
	cs := CompleteSuperProperties(doc, "\t$super: properties: ", Options{Definitions: webLookup})
	assert.ElementsMatch(t, []string{"image", "probe", "replicas", "resources"}, labels(cs))
	for _, c := range cs {
		if c.Label == "image" {
			assert.Equal(t, "Image to run", c.Doc)
		}
	}
	assert.ElementsMatch(t, []string{"image"}, labels(CompleteSuperProperties(doc, "\t$super: properties: im", Options{Definitions: webLookup})))
}
