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

package preview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const withStatus = `"web": {
	type: "component"
	attributes: {
		workload: type: "autodetects.core.oam.dev"
		status: {
			healthPolicy: "isHealth: (*context.output.status.readyReplicas | 0) == context.output.spec.replicas"
			customStatus: "message: \"\\(*context.output.status.readyReplicas | 0)/\\(context.output.spec.replicas) ready\""
		}
	}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		spec: {
			replicas: parameter.replicas
			selector: matchLabels: app: context.name
			template: {
				metadata: labels: app: context.name
				spec: containers: [{name: "web", image: "nginx"}]
			}
		}
	}
	parameter: replicas: *2 | int
}
`

func TestPreviewStatus(t *testing.T) {
	path := "/tmp/web.cue"
	res := Render(context.Background(), Request{Path: path, Source: []byte(withStatus), Values: []byte("observed:\n  output:\n    status:\n      readyReplicas: 1\n")})
	require.Empty(t, res.Error)
	require.NotNil(t, res.Status)
	assert.False(t, res.Status.Healthy)
	assert.Equal(t, "1/2 ready", res.Status.Message)

	res = Render(context.Background(), Request{Path: path, Source: []byte(withStatus), Values: []byte("parameter:\n  replicas: 1\nobserved:\n  output:\n    status:\n      readyReplicas: 1\n")})
	require.NotNil(t, res.Status)
	assert.True(t, res.Status.Healthy)

	res = Render(context.Background(), Request{Path: path, Source: []byte(withStatus)})
	require.NotNil(t, res.Status, "without observed state, the status of what renders")
	assert.Equal(t, "0/2 ready", res.Status.Message)

	noStatus := Render(context.Background(), Request{Path: path, Source: []byte(withStatus[:0] + `"cm": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: output: {apiVersion: "v1", kind: "ConfigMap", metadata: name: "x"}
`)})
	assert.Nil(t, noStatus.Status, "a definition with no status says nothing of one")

	skeleton, err := Skeleton(context.Background(), path, []byte(withStatus))
	require.NoError(t, err)
	assert.Contains(t, skeleton, "observed:")
}
