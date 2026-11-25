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

package application

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlEvent "sigs.k8s.io/controller-runtime/pkg/event"
	ctrlHandler "sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// watchSigningKeys reconciles every dispatched Application when the active signing key changes,
// so each re-signs its Components straight away rather than on its next resync. The Secret has
// a cache of its own, so the controller caches no other Secret.
func (r *Reconciler) watchSigningKeys(mgr ctrl.Manager, namespace string) (source.Source, error) {
	keys, err := ctrlcache.New(mgr.GetConfig(), ctrlcache.Options{
		Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper(), HTTPClient: mgr.GetHTTPClient(),
		ByObject: map[client.Object]ctrlcache.ByObject{&corev1.Secret{}: {
			Namespaces: map[string]ctrlcache.Config{namespace: {}},
			Field:      fields.OneTermEqualSelector("metadata.name", componentdispatch.SigningKeysSecret),
		}},
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Add(keys); err != nil {
		return nil, err
	}
	return source.Kind(keys, &corev1.Secret{},
		ctrlHandler.TypedEnqueueRequestsFromMapFunc(r.dispatchedApplications), activeKeyChanged()), nil
}

// activeKeyChanged passes an update to the signing keys that makes another key the active one,
// or removes a key: a revoked key's Components are signed again at once.
func activeKeyChanged() predicate.TypedFuncs[*corev1.Secret] {
	return predicate.TypedFuncs[*corev1.Secret]{
		CreateFunc: func(ctrlEvent.TypedCreateEvent[*corev1.Secret]) bool { return false },
		UpdateFunc: func(e ctrlEvent.TypedUpdateEvent[*corev1.Secret]) bool {
			if componentdispatch.ActiveKeyID(e.ObjectOld) != componentdispatch.ActiveKeyID(e.ObjectNew) {
				return true
			}
			for id := range e.ObjectOld.Data {
				if _, ok := e.ObjectNew.Data[id]; !ok {
					return true
				}
			}
			return false
		},
		DeleteFunc:  func(ctrlEvent.TypedDeleteEvent[*corev1.Secret]) bool { return false },
		GenericFunc: func(ctrlEvent.TypedGenericEvent[*corev1.Secret]) bool { return false },
	}
}

// dispatchedApplications is every Application that dispatches its components as Components.
func (r *Reconciler) dispatchedApplications(ctx context.Context, _ *corev1.Secret) []reconcile.Request {
	apps := &v1beta1.ApplicationList{}
	if err := r.List(ctx, apps); err != nil {
		klog.ErrorS(err, "Listing Applications to sign their Components with the new key")
		return nil
	}
	var requests []reconcile.Request
	for i := range apps.Items {
		if apps.Items[i].Annotations[oam.AnnotationDispatch] == oam.DispatchComponent {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&apps.Items[i])})
		}
	}
	return requests
}
