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

package features

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPublish(t *testing.T) {
	ctx := context.Background()
	gate := featuregate.NewFeatureGate()
	require.NoError(t, gate.Add(map[featuregate.Feature]featuregate.FeatureSpec{
		"On":  {Default: true},
		"Off": {Default: false},
	}))
	cli := fake.NewClientBuilder().Build()
	read := func() *corev1.ConfigMap {
		cm := &corev1.ConfigMap{}
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Namespace: "vela-system", Name: FeatureGatesConfigMapName}, cm))
		return cm
	}

	require.NoError(t, Publish(ctx, cli, "vela-system", "v1.12.0", gate))
	cm := read()
	assert.Equal(t, map[string]string{"On": "true", "Off": "false"}, cm.Data, "every gate it knows, none of the AllAlpha/AllBeta switches")
	assert.Equal(t, "v1.12.0", cm.Annotations[FeatureGatesVersionAnnotation])

	require.NoError(t, gate.Set("Off=true"))
	require.NoError(t, Publish(ctx, cli, "vela-system", "v1.12.1", gate))
	cm = read()
	assert.Equal(t, "true", cm.Data["Off"], "a restart with other flags is written over the last")
	assert.Equal(t, "v1.12.1", cm.Annotations[FeatureGatesVersionAnnotation])
}
