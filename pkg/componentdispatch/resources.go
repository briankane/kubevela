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

	pkgmulticluster "github.com/kubevela/pkg/multicluster"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// ExpandComponents lists an Application's resources as its users see them: each Component
// its tracker records is replaced by the resources vela-agent reports in that Component's
// status, in the Component's cluster, and the Component's payload Secret is left out.
// Other records pass through unchanged.
//
// A Component that no longer exists contributes nothing. One that cannot be read is listed
// as itself, so a cluster that is down still shows where the resources should be.
func ExpandComponents(ctx context.Context, cli client.Reader, mrs []v1beta1.ManagedResource) []v1beta1.ManagedResource {
	type secretKey struct{ cluster, namespace, name string }
	payloads := map[secretKey]bool{}
	expanded := make(map[int][]v1beta1.ManagedResource)
	for i, mr := range mrs {
		if !isComponent(mr) {
			continue
		}
		comp := &v1alpha1.Component{}
		err := cli.Get(clusterContext(ctx, mr.Cluster), client.ObjectKey{Namespace: mr.Namespace, Name: mr.Name}, comp)
		switch {
		case kerrors.IsNotFound(err):
			expanded[i] = []v1beta1.ManagedResource{}
		case err != nil:
			continue
		default:
			if comp.Spec.Payload != nil {
				payloads[secretKey{mr.Cluster, mr.Namespace, comp.Spec.Payload.SecretRef.Name}] = true
			}
			out := make([]v1beta1.ManagedResource, 0, len(comp.Status.Resources))
			for _, res := range comp.Status.Resources {
				out = append(out, v1beta1.ManagedResource{
					ClusterObjectReference: common.ClusterObjectReference{
						Cluster: mr.Cluster,
						ObjectReference: corev1.ObjectReference{
							APIVersion: res.APIVersion, Kind: res.Kind, Namespace: res.Namespace, Name: res.Name,
						},
					},
					OAMObjectReference: common.OAMObjectReference{Component: mr.Component, Trait: res.Definition.Trait()},
				})
			}
			expanded[i] = out
		}
	}
	result := make([]v1beta1.ManagedResource, 0, len(mrs))
	for i, mr := range mrs {
		if out, ok := expanded[i]; ok {
			result = append(result, out...)
			continue
		}
		if mr.Kind == "Secret" && mr.APIVersion == "v1" && payloads[secretKey{mr.Cluster, mr.Namespace, mr.Name}] {
			continue
		}
		result = append(result, mr)
	}
	return result
}

func isComponent(mr v1beta1.ManagedResource) bool {
	return mr.Kind == v1alpha1.ComponentKind && mr.APIVersion == v1alpha1.SchemeGroupVersion.String()
}

func clusterContext(ctx context.Context, cluster string) context.Context {
	if cluster == "" {
		cluster = pkgmulticluster.Local
	}
	return pkgmulticluster.WithCluster(ctx, cluster)
}
