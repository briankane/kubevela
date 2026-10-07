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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Each step's debug data is in a ConfigMap named for the Application, the
// step's id and the end of the Application's UID, as the debug policy writes
// it; a suspend or step group records none.
func TestDebugConfigMaps(t *testing.T) {
	app := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "shop", "uid": "580ceafe-0ef9-4df7-a3ea-c3d93f9ee363"},
		"status": map[string]interface{}{"workflow": map[string]interface{}{"steps": []interface{}{
			map[string]interface{}{"id": "a1", "name": "web", "type": "apply-component", "phase": "succeeded"},
			map[string]interface{}{"id": "s1", "name": "wait", "type": "suspend", "phase": "succeeded"},
			map[string]interface{}{"id": "g1", "name": "group", "type": "step-group", "phase": "failed", "subSteps": []interface{}{
				map[string]interface{}{"id": "b1", "name": "notify", "type": "notification", "phase": "failed"},
			}},
		}}},
	}}
	assert.Equal(t, []debugStep{
		{Name: "web", Phase: "succeeded", ConfigMap: "shop-a1-debug-ee363"},
		{Name: "notify", Phase: "failed", ConfigMap: "shop-b1-debug-ee363"},
	}, debugConfigMaps(app))
}

// vela/debugData answers with what the cluster's debug ConfigMaps hold.
func TestDebugDataRequest(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.DebugData = func(namespace, name string) ([]debugStep, error) {
			if namespace != "default" || name != "shop" {
				return nil, errors.New("not found")
			}
			return []debugStep{{Name: "web", Phase: "succeeded", ConfigMap: "shop-a1-debug-ee363", Debug: "output: {}\n"}}, nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	m := c.response(c.send(MethodDebugData, DebugDataParams{Namespace: "default", Name: "shop"}, true))
	var r DebugDataResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, "k3d-test", r.Context)
	require.Len(t, r.Steps, 1)
	assert.Equal(t, "output: {}\n", r.Steps[0].Debug)

	m = c.response(c.send(MethodDebugData, DebugDataParams{Namespace: "default", Name: "nope"}, true))
	assert.Contains(t, string(m["error"]), "could not read default/nope")
}

// vela/locate finds a field in a file on disk.
func TestLocateRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "web.cue")
	require.NoError(t, os.WriteFile(file, []byte(parentSrc), 0600))
	c := newClient(t)
	c.drain()
	m := c.response(c.send(MethodLocate, LocateParams{TextDocument: TextDocumentIdentifier{URI: "file://" + file}, Path: "template.parameter.image"}, true))
	var r Range
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, uint32(8), r.Start.Line, "image, in the parameter")

	m = c.response(c.send(MethodLocate, LocateParams{TextDocument: TextDocumentIdentifier{URI: "file://" + file}, Path: "nothing"}, true))
	assert.Equal(t, "null", string(m["result"]))
}
