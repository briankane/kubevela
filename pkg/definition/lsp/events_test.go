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

func event(kind, name, typ, reason, message, last string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata":       map[string]interface{}{"name": name + "." + reason, "uid": name + reason},
		"involvedObject": map[string]interface{}{"kind": kind, "name": name},
		"type":           typ, "reason": reason, "message": message, "lastTimestamp": last, "count": int64(2),
	}}
}

// An event is an Application's when it is about the Application, a resource
// it applied, or what such a resource made, named after it.
func TestEventMatches(t *testing.T) {
	names := []string{"shop", "web"}
	assert.True(t, eventMatches(event("Application", "shop", "Normal", "Parsed", "", ""), names))
	assert.True(t, eventMatches(event("Deployment", "web", "Normal", "ScalingReplicaSet", "", ""), names))
	assert.True(t, eventMatches(event("Pod", "web-7d9f8-x2k4q", "Warning", "BackOff", "", ""), names), "a pod the Deployment made")
	assert.True(t, eventMatches(event("Pod", "web-0", "Normal", "Started", "", ""), names), "a StatefulSet's pod")
	assert.False(t, eventMatches(event("Deployment", "web-api", "Normal", "ScalingReplicaSet", "", ""), names), "another resource, named after it")
	assert.False(t, eventMatches(event("Pod", "webhook-1", "Normal", "Started", "", ""), names))
	assert.False(t, eventMatches(event("Pod", "other", "Normal", "Started", "", ""), names))
}

// Watching events sends the Application's, newest first, as they change.
func TestWatchEvents(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Events = func(ctx context.Context, cluster, namespace string, each func([]*unstructured.Unstructured)) {
			if cluster == "eu-1" {
				each([]*unstructured.Unstructured{event("Pod", "web-7", "Warning", "Failed", "ImagePullBackOff", "2026-10-07T18:50:00Z")})
			} else {
				each([]*unstructured.Unstructured{
					event("Application", "shop", "Normal", "Parsed", "Parsed successfully", "2026-10-07T18:43:05Z"),
					event("Pod", "web-1", "Warning", "BackOff", "Back-off restarting", "2026-10-07T18:44:00Z"),
					event("Pod", "other", "Normal", "Started", "", "2026-10-07T18:45:00Z"),
				})
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

	m := c.response(c.send(MethodWatchEvents, WatchEventsParams{Namespace: "default", Application: "shop", Names: []string{"shop", "web"}, Clusters: []string{"eu-1"}}, true))
	require.Empty(t, string(m["error"]))
	var got EventsChanged
	for len(got.Events) < 3 {
		n, ok := c.tryRead(5 * time.Second)
		require.True(t, ok, "no events from both clusters")
		if string(n["method"]) == `"`+MethodEventsChanged+`"` {
			require.NoError(t, json.Unmarshal(n["params"], &got))
		}
	}
	assert.Equal(t, "shop", got.Application)
	require.Len(t, got.Events, 3, "the other pod's is not the Application's")
	assert.Equal(t, "Failed", got.Events[0].Reason, "newest first, whichever cluster")
	assert.Equal(t, "eu-1", got.Events[0].Cluster)
	assert.Equal(t, "BackOff", got.Events[1].Reason)
	assert.Equal(t, "", got.Events[1].Cluster, "the hub's is not marked")
	assert.Equal(t, "Warning", got.Events[1].Type)
	assert.Equal(t, "Pod/web-1", got.Events[1].Object)
	assert.Equal(t, int64(2), got.Events[1].Count)
	c.response(c.send(MethodUnwatchEvents, WatchEventsParams{Namespace: "default", Application: "shop"}, true))
}
