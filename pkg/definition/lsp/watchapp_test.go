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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// changedTo reads notifications until an Application change arrives.
func changedTo(t *testing.T, c *client) ApplicationChanged {
	t.Helper()
	for {
		m, ok := c.tryRead(5 * time.Second)
		require.True(t, ok, "no application change")
		if string(m["method"]) == `"`+MethodApplicationChanged+`"` {
			var ch ApplicationChanged
			require.NoError(t, json.Unmarshal(m["params"], &ch))
			return ch
		}
	}
}

// Watching an Application sends each version of it as it changes, until
// the watch is ended.
func TestWatchApplication(t *testing.T) {
	stopped := make(chan struct{})
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Watch = func(ctx context.Context, namespace, name string, each func(*unstructured.Unstructured)) {
			for _, phase := range []string{"executing", "succeeded"} {
				app := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": name, "namespace": namespace}}}
				_ = unstructured.SetNestedField(app.Object, phase, "status", "workflow", "status")
				each(app)
			}
			<-ctx.Done()
			close(stopped)
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	m := c.response(c.send(MethodWatchApplication, WatchApplicationParams{Namespace: "default", Name: "shop"}, true))
	require.Empty(t, string(m["error"]))
	var phases []string
	for range 2 {
		ch := changedTo(t, c)
		assert.Equal(t, "shop", ch.Name)
		phase, _, _ := unstructured.NestedString(ch.Application, "status", "workflow", "status")
		phases = append(phases, phase)
	}
	assert.Equal(t, []string{"executing", "succeeded"}, phases)

	c.response(c.send(MethodUnwatchApplication, WatchApplicationParams{Namespace: "default", Name: "shop"}, true))
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch was not ended")
	}
}
