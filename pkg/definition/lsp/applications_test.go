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

func appObject(t *testing.T, yaml string) *unstructured.Unstructured {
	t.Helper()
	var obj map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(yaml), &obj))
	return &unstructured.Unstructured{Object: obj}
}

// An Application's summary is what the tree shows of it: its health, where
// its workflow is, and why it stopped.
func TestSummarize(t *testing.T) {
	cases := map[string]struct {
		app  string
		want AppSummary
	}{
		"running, on its second step": {
			app: `{"metadata":{"name":"shop","namespace":"default","generation":2},"status":{"observedGeneration":2,"status":"runningWorkflow",
				"services":[{"name":"db","healthy":true},{"name":"web","healthy":false}],
				"workflow":{"steps":[{"name":"db","phase":"succeeded"},{"name":"web","phase":"running"}]}}}`,
			want: AppSummary{Namespace: "default", Name: "shop", Phase: "runningWorkflow", Components: 2, Healthy: false, Step: "web", StepPhase: "running"},
		},
		"suspended at a step": {
			app: `{"metadata":{"name":"gated","namespace":"default"},"status":{"status":"workflowSuspending",
				"workflow":{"suspend":true,"steps":[{"name":"approve","phase":"suspending"}]}}}`,
			want: AppSummary{Namespace: "default", Name: "gated", Phase: "workflowSuspending", Suspended: true, Step: "approve", StepPhase: "suspending"},
		},
		"a failed step is named over a later one": {
			app: `{"metadata":{"name":"f","namespace":"x"},"status":{"status":"runningWorkflow",
				"workflow":{"message":"boom","steps":[{"name":"a","phase":"failed"},{"name":"b","phase":"pending"}]}}}`,
			want: AppSummary{Namespace: "x", Name: "f", Phase: "runningWorkflow", Step: "a", StepPhase: "failed", Message: "boom"},
		},
		"healthy and done, an addon's": {
			app: `{"metadata":{"name":"addon-x","namespace":"vela-system","labels":{"addons.oam.dev/name":"x"}},"status":{"status":"running",
				"services":[{"name":"c","healthy":true}],"workflow":{"finished":true,"steps":[{"name":"c","phase":"succeeded"}]}}}`,
			want: AppSummary{Namespace: "vela-system", Name: "addon-x", Phase: "running", Components: 1, Healthy: true, Finished: true, Addon: "x"},
		},
		"being deleted": {
			app:  `{"metadata":{"name":"d","namespace":"x","deletionTimestamp":"2026-10-08T12:00:00Z"},"status":{"status":"deleting"}}`,
			want: AppSummary{Namespace: "x", Name: "d", Phase: "deleting", Deleting: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, summarize(appObject(t, tc.app)))
		})
	}
}

// applicationsChanged reads notifications until the Applications arrive.
func applicationsChanged(t *testing.T, c *client) ApplicationsChanged {
	t.Helper()
	for {
		m, ok := c.tryRead(5 * time.Second)
		require.True(t, ok, "no applications")
		if string(m["method"]) == `"`+MethodApplicationsChanged+`"` {
			var ch ApplicationsChanged
			require.NoError(t, json.Unmarshal(m["params"], &ch))
			return ch
		}
	}
}

// Watching the Applications sends them all, summarised and sorted, as they
// change, until the watch is ended.
func TestWatchApplications(t *testing.T) {
	stopped := make(chan struct{})
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Applications = func(ctx context.Context, each func([]*unstructured.Unstructured)) {
			web := appObject(t, `{"metadata":{"name":"web","namespace":"default"},"status":{"status":"running"}}`)
			addon := appObject(t, `{"metadata":{"name":"addon-a","namespace":"vela-system"},"status":{"status":"running"}}`)
			each([]*unstructured.Unstructured{web, addon})
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

	m := c.response(c.send(MethodWatchApplications, struct{}{}, true))
	require.Empty(t, string(m["error"]))
	ch := applicationsChanged(t, c)
	var names []string
	for _, a := range ch.Applications {
		names = append(names, a.Namespace+"/"+a.Name)
	}
	assert.Equal(t, []string{"default/web", "vela-system/addon-a"}, names)
	assert.NotEmpty(t, ch.Context)

	c.response(c.send(MethodUnwatchApplications, struct{}{}, true))
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch was not ended")
	}
}
