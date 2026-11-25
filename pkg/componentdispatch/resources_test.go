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

package componentdispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

func record(cluster, apiVersion, kind, name, component string) v1beta1.ManagedResource {
	return v1beta1.ManagedResource{
		ClusterObjectReference: common.ClusterObjectReference{Cluster: cluster, ObjectReference: corev1.ObjectReference{
			APIVersion: apiVersion, Kind: kind, Namespace: "team-a", Name: name,
		}},
		OAMObjectReference: common.OAMObjectReference{Component: component},
	}
}

func dispatched(name string, resources ...v1alpha1.ComponentAppliedResource) *v1alpha1.Component {
	return &v1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: name},
		Spec: v1alpha1.ComponentSpec{Payload: &v1alpha1.ComponentPayload{
			SecretRef: v1alpha1.ComponentSecretReference{Name: name + "-payload"},
		}},
		Status: v1alpha1.ComponentStatus{Resources: resources},
	}
}

func readerWith(t *testing.T, objs ...client.Object) client.WithWatch {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestListsWhatVelaAgentAppliedInPlaceOfEachComponent(t *testing.T) {
	cli := readerWith(t, dispatched("shop-web-worker",
		v1alpha1.ComponentAppliedResource{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "web", Role: "output"},
		v1alpha1.ComponentAppliedResource{APIVersion: "v1", Kind: "Service", Namespace: "team-a", Name: "web", Role: "outputs.service", Definition: v1alpha1.ComponentResourceDefinition{Kind: v1alpha1.DefinitionTrait, Type: "gateway"}},
	))
	got := ExpandComponents(context.Background(), cli, []v1beta1.ManagedResource{
		record("worker", v1alpha1.SchemeGroupVersion.String(), v1alpha1.ComponentKind, "shop-web-worker", "web"),
		record("worker", "v1", "Secret", "shop-web-worker-payload", "web"),
		record("", "v1", "ConfigMap", "in-process", "db"),
	})

	deployment := record("worker", "apps/v1", "Deployment", "web", "web")
	service := record("worker", "v1", "Service", "web", "web")
	service.Trait = "gateway"
	require.Equal(t, []v1beta1.ManagedResource{deployment, service, record("", "v1", "ConfigMap", "in-process", "db")}, got,
		"the Component becomes what it applied, in its cluster; its payload Secret goes; other records stay")
}

func TestAComponentThatIsGoneListsNothing(t *testing.T) {
	got := ExpandComponents(context.Background(), readerWith(t), []v1beta1.ManagedResource{
		record("worker", v1alpha1.SchemeGroupVersion.String(), v1alpha1.ComponentKind, "shop-web-worker", "web"),
	})
	require.Empty(t, got)
}

func TestAComponentThatCannotBeReadIsListedAsItself(t *testing.T) {
	cli := interceptor.NewClient(readerWith(t), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("cluster worker is unreachable")
		},
	})
	comp := record("worker", v1alpha1.SchemeGroupVersion.String(), v1alpha1.ComponentKind, "shop-web-worker", "web")
	payload := record("worker", "v1", "Secret", "shop-web-worker-payload", "web")
	got := ExpandComponents(context.Background(), cli, []v1beta1.ManagedResource{comp, payload})
	require.Equal(t, []v1beta1.ManagedResource{comp, payload}, got)
}
