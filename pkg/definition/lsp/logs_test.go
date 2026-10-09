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

// A workload's pods are found by its selector; a pod is its own.
func TestPodSelector(t *testing.T) {
	deploy := unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Deployment", "metadata": map[string]interface{}{"name": "web"},
		"spec": map[string]interface{}{"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "web", "tier": "front"}}},
	}}
	sel, ok := podSelector(deploy)
	require.True(t, ok)
	assert.Equal(t, "app=web,tier=front", sel)

	job := unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Job", "metadata": map[string]interface{}{"name": "migrate", "uid": "u-1"},
		"spec": map[string]interface{}{"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"batch.kubernetes.io/controller-uid": "u-1"}}},
	}}
	sel, ok = podSelector(job)
	require.True(t, ok)
	assert.Equal(t, "batch.kubernetes.io/controller-uid=u-1", sel)

	_, ok = podSelector(unstructured.Unstructured{Object: map[string]interface{}{"kind": "ConfigMap"}})
	assert.False(t, ok, "a ConfigMap has no pods")
}

// Watching logs sends the lines of an Application's pods as they are written.
func TestWatchLogs(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Logs = func(ctx context.Context, cluster, namespace string, workloads []LogWorkload, each func([]LogLine)) {
			if len(workloads) == 1 && workloads[0].Name == "web" {
				each([]LogLine{{Pod: "web-1", Container: "web", Component: "web", Text: "listening on :8080 in " + cluster}})
			}
			<-ctx.Done()
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	web := LogWorkload{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}
	spoke := web
	spoke.Cluster = "eu-1"
	m := c.response(c.send(MethodWatchLogs, WatchLogsParams{Namespace: "default", Application: "shop", Workloads: []LogWorkload{web, spoke}}, true))
	require.Empty(t, string(m["error"]))
	got := map[string]string{}
	for len(got) < 2 {
		n, ok := c.tryRead(5 * time.Second)
		require.True(t, ok, "no logs from both clusters")
		if string(n["method"]) == `"`+MethodLogs+`"` {
			var w LogsWritten
			require.NoError(t, json.Unmarshal(n["params"], &w))
			assert.Equal(t, "shop", w.Application)
			for _, l := range w.Lines {
				got[l.Cluster] = l.Text
			}
		}
	}
	assert.Equal(t, map[string]string{"": "listening on :8080 in local", "eu-1": "listening on :8080 in eu-1"}, got, "each cluster's workloads followed there, a member's lines marked")
	c.response(c.send(MethodUnwatchLogs, WatchLogsParams{Namespace: "default", Application: "shop"}, true))
}
