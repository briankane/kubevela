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

package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
)

func TestComponentIsRegistered(t *testing.T) {
	for _, obj := range []runtime.Object{&Component{}, &ComponentList{}} {
		gvks, _, err := k8sscheme.Scheme.ObjectKinds(obj)
		require.NoError(t, err)
		require.Len(t, gvks, 1)
		require.Equal(t, SchemeGroupVersion, gvks[0].GroupVersion())
	}
	require.Equal(t, "Component", ComponentGroupVersionKind.Kind)
}

func TestComponentRoundTrip(t *testing.T) {
	in := &Component{
		TypeMeta:   metav1.TypeMeta{APIVersion: SchemeGroupVersion.String(), Kind: ComponentKind},
		ObjectMeta: metav1.ObjectMeta{Name: "shop-backend-prod-use1", Namespace: "team-a"},
		Spec: ComponentSpec{
			Target:   ComponentTarget{Cluster: "prod-use1"},
			Revision: 7,
			Resources: []ComponentResource{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "backend", Role: "output", SHA256: "9f2c"},
				{APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "team-a", Name: "backend", Role: "outputs.ingress", Definition: ComponentResourceDefinition{Kind: DefinitionTrait, Type: "gateway"}, SHA256: "77d1"},
			},
			Apply: ComponentApply{
				ApplyOnce:      &ApplyOncePolicySpec{Enable: true},
				GarbageCollect: &GarbageCollectPolicySpec{KeepLegacyResource: true},
				TakeOver:       &TakeOverPolicySpec{},
				ReadOnly:       &ReadOnlyPolicySpec{},
			},
			Health: ComponentHealth{
				Context: map[string]string{"name": "backend", "appName": "shop"},
				Workload: &ComponentHealthSubject{
					HealthSnippets: HealthSnippets{HealthPolicy: "isHealth: true", CustomStatus: `message: "ok"`},
					Parameter:      &runtime.RawExtension{Raw: []byte(`{"replicas":2}`)},
					Ancestors:      []HealthSnippets{{HealthPolicy: "isHealth: false"}},
				},
				Traits: []ComponentTraitHealth{{Type: "gateway", ComponentHealthSubject: ComponentHealthSubject{
					HealthSnippets: HealthSnippets{HealthPolicy: "isHealth: true"},
				}}},
			},
			Observe: []ComponentObservation{{Resource: "output", Name: "readyReplicas", Path: ".status.readyReplicas"}},
			Payload: &ComponentPayload{
				SecretRef: ComponentSecretReference{Name: "shop-backend-prod-use1-payload"},
				Format:    "gzip+json/v1",
				SHA256:    "5be0",
			},
			Signature: ComponentSignature{KeyID: "hub-2026-11", Value: "c2ln"},
		},
		Status: ComponentStatus{
			ObservedRevision: 7,
			Conditions:       []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Signed", LastTransitionTime: metav1.Unix(1, 0).Rfc3339Copy()}},
			Health:           &common.ApplicationComponentStatus{Name: "backend", Healthy: true},
			Observed:         map[string]runtime.RawExtension{"output": {Raw: []byte(`{"readyReplicas":2}`)}},
		},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)
	out := &Component{}
	require.NoError(t, json.Unmarshal(data, out))
	require.Equal(t, in, out)
	require.Equal(t, in, in.DeepCopy())
}

func TestComponentJSONNames(t *testing.T) {
	data, err := json.Marshal(ComponentSpec{
		Delete: true,
		Apply:  ComponentApply{GarbageCollect: &GarbageCollectPolicySpec{}, SharedResource: &SharedResourcePolicySpec{}, ResourceUpdate: &ResourceUpdatePolicySpec{}},
	})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Contains(t, fields, "delete")
	require.NotContains(t, fields, "payload", "a tombstone carries no payload")
	require.ElementsMatch(t, []string{"garbageCollect", "sharedResource", "resourceUpdate"}, keys(fields["apply"].(map[string]any)))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
