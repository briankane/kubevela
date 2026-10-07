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
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An applied resource is read as YAML, without its managed fields.
func TestResourceAsYAML(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "web", "namespace": "default", "managedFields": []interface{}{map[string]interface{}{"manager": "x"}}},
		"data":     map[string]interface{}{"a": "b"},
	}}
	out, err := resourceYAML(obj)
	require.NoError(t, err)
	assert.Contains(t, out, "kind: ConfigMap")
	assert.Contains(t, out, "a: b")
	assert.NotContains(t, out, "managedFields")
}

// vela/resource reads one through the cluster.
func TestResourceRequest(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Resource = func(apiVersion, kind, namespace, name string) (string, error) {
			if kind != "ConfigMap" || name != "web" {
				return "", errors.New("not found")
			}
			return "kind: ConfigMap\n", nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	m := c.response(c.send(MethodResource, ResourceParams{APIVersion: "v1", Kind: "ConfigMap", Namespace: "default", Name: "web"}, true))
	var r ResourceResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, "kind: ConfigMap\n", r.YAML)
	assert.Equal(t, "k3d-test", r.Context)

	m = c.response(c.send(MethodResource, ResourceParams{APIVersion: "v1", Kind: "ConfigMap", Namespace: "default", Name: "nope"}, true))
	assert.Contains(t, string(m["error"]), "not found")
	m = c.response(c.send(MethodResource, ResourceParams{APIVersion: "v1", Kind: "ConfigMap", Namespace: "default", Name: "web", Cluster: "eu-west"}, true))
	assert.Contains(t, string(m["error"]), "eu-west", "a member cluster's resource is not read")
}
