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

package appkeeper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
	"github.com/oam-dev/kubevela/pkg/resourcetracker"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// An Application's sharer key keeps its pre-kind form (<namespace>/<name>), so a resource an
// Application shares with a Component holds both forms at once.
func TestAnApplicationAndAComponentShareAResource(t *testing.T) {
	defer func(p float64) { resourcekeeper.MarkWithProbability = p }(resourcekeeper.MarkWithProbability)
	resourcekeeper.MarkWithProbability = 1.0
	r := require.New(t)
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	shared := resourcekeeper.Policies{SharedResource: &v1alpha1.SharedResourcePolicySpec{
		Rules: []v1alpha1.SharedResourcePolicyRule{{Selector: v1alpha1.ResourcePolicyRuleSelector{ResourceTypes: []string{"ConfigMap"}}}},
	}}
	appKeeper := func(app *v1beta1.Application) resourcekeeper.ResourceKeeper {
		rk, err := resourcekeeper.New(ctx, cli, NewAppResourceTracker(app), shared, resourcekeeper.Options{})
		r.NoError(err)
		return rk
	}
	component, err := resourcekeeper.New(ctx, cli, resourcetracker.NewBase(resourcetracker.Owner{Kind: "Component", Namespace: "default", Name: "backend", UID: "uid-backend", Generation: 1}), shared, resourcekeeper.Options{})
	r.NoError(err)
	get := func() *corev1.ConfigMap {
		cm := &corev1.ConfigMap{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: "default", Name: "shared"}, cm); err != nil {
			r.True(kerrors.IsNotFound(err))
			return nil
		}
		return cm
	}

	r.NoError(appKeeper(shopApp(1)).Dispatch(ctx, []*unstructured.Unstructured{renderedConfigMap("shared")}, nil))
	r.NoError(component.Dispatch(ctx, []*unstructured.Unstructured{renderedConfigMap("shared")}, nil))
	r.Equal("default/shop,Component/default/backend", get().Annotations[oam.AnnotationAppSharedBy])

	// the Application goes: the Component takes over
	deleting := shopApp(1)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	finished := false
	for i := 0; i < 5 && !finished; i++ {
		finished, _, err = appKeeper(deleting).GarbageCollect(ctx)
		r.NoError(err)
	}
	r.True(finished)
	cm := get()
	r.NotNil(cm, "the Component still shares it")
	r.Equal("Component/default/backend", cm.Annotations[oam.AnnotationAppSharedBy])
	r.Equal("Component", cm.Labels[oam.LabelOwnerKind])
	r.Equal("backend", cm.Labels[oam.LabelOwnerName])

	// the app label stays for display, but control is the Component's: a new Application of
	// the same name that does not share is refused
	recreated, err := resourcekeeper.New(ctx, cli, NewAppResourceTracker(shopApp(1)), resourcekeeper.Policies{}, resourcekeeper.Options{})
	r.NoError(err)
	err = recreated.Dispatch(ctx, []*unstructured.Unstructured{renderedConfigMap("shared")}, nil)
	r.ErrorContains(err, "Component/default/backend")
}
