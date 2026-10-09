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

func joined() *JoinedClusters {
	return &JoinedClusters{Context: "k3d-vela", Clusters: []JoinedCluster{
		{Name: "local"},
		{Name: "eu-1", Labels: map[string]string{"region": "eu", "tier": "prod"}},
		{Name: "us-1", Labels: map[string]string{"region": "us", "tier": "prod"}},
	}}
}

const topologyApp = `apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: shop
spec:
  components:
    - name: web
      type: webservice
      properties:
        image: nginx
  policies:
    - name: where
      type: topology
      properties:
TOPOLOGY`

func topology(props string) string {
	return strings.Replace(topologyApp, "TOPOLOGY", props, 1)
}

// topologyFindings are the findings about joined clusters, as "line: message".
func topologyFindings(diags []Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if strings.Contains(d.Message, "cluster") {
			out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
		}
	}
	return out
}

func TestTopologyChecks(t *testing.T) {
	cases := map[string]struct {
		props string
		want  []string
	}{
		"joined clusters":                 {props: "        clusters: [local, eu-1]\n"},
		"a block list of joined clusters": {props: "        clusters:\n          - eu-1\n          - us-1\n"},
		"a cluster not joined": {
			props: "        clusters:\n          - eu-1\n          - eu-2\n",
			want:  []string{"17: no cluster named eu-2 is joined to k3d-vela"},
		},
		"an expression is not judged":     {props: "        clusters: [\"$(context.cluster)\"]\n"},
		"a selector some cluster matches": {props: "        clusterLabelSelector:\n          region: eu\n"},
		"a selector no cluster matches": {
			props: "        clusterLabelSelector:\n          region: eu\n          tier: dev\n",
			want:  []string{"15: no cluster joined to k3d-vela has the labels region=eu, tier=dev"},
		},
		"the deprecated clusterSelector, matching none": {
			props: "        clusterSelector:\n          region: ap\n",
			want:  []string{"15: no cluster joined to k3d-vela has the labels region=ap"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diags, ok := CheckApplicationFile("app.yaml", []byte(topology(tc.props)), Options{Clusters: joined()})
			require.True(t, ok)
			assert.Equal(t, tc.want, topologyFindings(diags))
		})
	}
	diags, _ := CheckApplicationFile("app.yaml", []byte(topology("        clusters: [eu-9]\n")), Options{})
	assert.Empty(t, topologyFindings(diags), "nothing is judged when the clusters are not known")
}

// A misspelt cluster is offered the one it most likely meant.
func TestTopologyFix(t *testing.T) {
	diags, _ := CheckApplicationFile("app.yaml", []byte(topology("        clusters: [eu-l]\n")), Options{Clusters: joined()})
	var fixes []Fix
	for _, d := range diags {
		if strings.Contains(d.Message, "eu-l") {
			fixes = d.Fixes
		}
	}
	require.Len(t, fixes, 1)
	assert.Equal(t, "Change to eu-1", fixes[0].Title)
}

func TestTopologyCompletion(t *testing.T) {
	labels := func(cs []Completion) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Label)
		}
		return out
	}
	complete := func(props string) []string {
		doc := topology(props)
		at := strings.Index(doc, "|")
		require.GreaterOrEqual(t, at, 0)
		doc = doc[:at] + doc[at+1:]
		cs, ok := CompleteYAMLFile("app.yaml", doc, at, Options{Applications: LayeredDefinitions{BuiltinDefinitions()}, Clusters: joined()})
		require.True(t, ok, props)
		return labels(cs)
	}
	assert.Equal(t, []string{"eu-1", "local", "us-1"}, complete("        clusters:\n          - |\n"))
	assert.Equal(t, []string{"eu-1"}, complete("        clusters:\n          - e|\n"))
	assert.Equal(t, []string{"region", "tier"}, complete("        clusterLabelSelector:\n          |\n"))
	assert.Equal(t, []string{"eu", "us"}, complete("        clusterLabelSelector:\n          region: |\n"))
}
