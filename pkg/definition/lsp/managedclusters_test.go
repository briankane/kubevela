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

package lsp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// The joined clusters are read from their Secrets, never with their credentials.
func TestReadClusters(t *testing.T) {
	spoke := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "eu-1", Namespace: "vela-system", Labels: map[string]string{
			"cluster.core.oam.dev/cluster-credential-type": "ServiceAccountToken",
			"region": "eu",
		}},
		Data: map[string][]byte{"endpoint": []byte("https://eu-1:6443"), "token": []byte("s3cret"), "ca.crt": []byte("ca")},
	}
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).WithObjects(spoke).Build()
	got, err := readClusters(context.Background(), cli)
	require.NoError(t, err)
	assert.Equal(t, []ManagedCluster{
		{Name: "local", Type: "Internal", Accepted: true},
		{Name: "eu-1", Type: "ServiceAccountToken", Endpoint: "https://eu-1:6443", Accepted: true, Labels: map[string]string{"region": "eu"}},
	}, got)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "s3cret")
}

func TestClustersRequest(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Clusters = func() ([]ManagedCluster, error) {
			return []ManagedCluster{{Name: "local", Type: "Internal", Accepted: true}}, nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	var got ClustersResult
	require.NoError(t, json.Unmarshal(c.response(c.send(MethodClusters, struct{}{}, true))["result"], &got))
	assert.Equal(t, ClustersResult{Context: "k3d-test", Clusters: []ManagedCluster{{Name: "local", Type: "Internal", Accepted: true}}}, got)
}

// A topology policy is checked against the clusters joined when the server
// connected, and against the list again whenever vela/clusters reads it.
func TestTopologyAgainstJoined(t *testing.T) {
	joined := []ManagedCluster{{Name: "local", Type: "Internal", Accepted: true}}
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Joined = joined
		c.Clusters = func() ([]ManagedCluster, error) {
			return append(joined, ManagedCluster{Name: "eu-1", Type: "ServiceAccountToken", Accepted: true}), nil
		}
		return c, err
	}
	app := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: a\nspec:\n  components: []\n  policies:\n    - name: where\n      type: topology\n      properties:\n        clusters: [eu-1]\n"
	warned := func(want bool) func([]Diagnostic) bool {
		return func(ds []Diagnostic) bool {
			for _, d := range ds {
				if strings.Contains(d.Message, "no cluster named eu-1") {
					return want
				}
			}
			return !want
		}
	}
	c, uri, diags := openWith(t, cluster, "auto", t.TempDir(), "app.yaml", app, warned(true))
	require.True(t, warned(true)(diags), "eu-1 is not joined when the server connects")

	c.response(c.send(MethodClusters, struct{}{}, true))
	require.True(t, warned(false)(settledUntil(c, uri, warned(false))), "once listed again, eu-1 is joined")
}
