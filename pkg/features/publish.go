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
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// FeatureGatesConfigMapName is the ConfigMap, in the controller's namespace,
	// where the controller publishes its feature gates when it starts: a key per
	// gate, "true" or "false". Other processes that run KubeVela code, such as
	// VelaUX, read it to behave as the controller does.
	FeatureGatesConfigMapName = "kubevela-feature-gates"
	// FeatureGatesVersionAnnotation is the KubeVela version that published them.
	FeatureGatesVersionAnnotation = "core.oam.dev/kubevela-version"
)

// GateStates are a gate's features and whether each is on, leaving out the
// AllAlpha and AllBeta switches every feature gate carries.
func GateStates(gate featuregate.FeatureGate) map[string]string {
	known := gate.DeepCopy().GetAll()
	names := make([]string, 0, len(known))
	for f := range known {
		if f == "AllAlpha" || f == "AllBeta" {
			continue
		}
		names = append(names, string(f))
	}
	sort.Strings(names)
	out := make(map[string]string, len(names))
	for _, name := range names {
		if gate.Enabled(featuregate.Feature(name)) {
			out[name] = "true"
		} else {
			out[name] = "false"
		}
	}
	return out
}

// Publish writes the gate's states to FeatureGatesConfigMapName in namespace,
// replacing what an earlier start wrote.
func Publish(ctx context.Context, cli client.Client, namespace, version string, gate featuregate.FeatureGate) error {
	cm := &corev1.ConfigMap{}
	err := cli.Get(ctx, types.NamespacedName{Namespace: namespace, Name: FeatureGatesConfigMapName}, cm)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	exists := err == nil
	if !exists {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FeatureGatesConfigMapName, Namespace: namespace}}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[FeatureGatesVersionAnnotation] = version
	cm.Data = GateStates(gate)
	if exists {
		return cli.Update(ctx, cm)
	}
	return cli.Create(ctx, cm)
}
