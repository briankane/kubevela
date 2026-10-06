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
	"fmt"
	"strings"
	"time"

	"github.com/kubevela/pkg/apis/cue/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// Cluster is what the editor reads of the kubeconfig's cluster.
type Cluster struct {
	// Fetch fetches the OpenAPI v3 document of one of its group-versions.
	Fetch kubeschema.Fetch
	// KubeVela is set when it runs KubeVela.
	KubeVela bool
	// Context is the kubeconfig context reached.
	Context string
	// Packages are its Package resources, in every namespace, as the
	// controller loads them.
	Packages []v1alpha1.Package
}

// ClusterConnector reaches the cluster the kubeconfig names.
type ClusterConnector func() (Cluster, error)

// clusterTimeout bounds each call to the cluster, so an unreachable one
// cannot hold an editor up.
const clusterTimeout = 3 * time.Second

// kubeVelaGroup is the API group a cluster running KubeVela serves.
const kubeVelaGroup = "core.oam.dev"

// connectKubeconfig is the ClusterConnector for the kubeconfig's current
// context, as kubectl reads it.
func connectKubeconfig() (Cluster, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		return Cluster{}, err
	}
	out := Cluster{Context: raw.CurrentContext}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return out, err
	}
	cfg.Timeout = clusterTimeout
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return out, err
	}
	groups, err := dc.ServerGroups()
	if err != nil {
		return out, err
	}
	packages := false
	for _, g := range groups.Groups {
		out.KubeVela = out.KubeVela || g.Name == kubeVelaGroup
		packages = packages || g.Name == v1alpha1.GroupVersion.Group
	}
	if packages {
		out.Packages = listPackages(cfg)
	}
	if !out.KubeVela {
		return out, nil
	}
	paths, err := dc.OpenAPIV3().Paths()
	if err != nil {
		return out, err
	}
	out.Fetch = func(gv string) ([]byte, error) {
		key := "api/" + gv
		if strings.Contains(gv, "/") {
			key = "apis/" + gv
		}
		p, ok := paths[key]
		if !ok {
			return nil, fmt.Errorf("the cluster serves no %s", gv)
		}
		return p.Schema("application/json")
	}
	return out, nil
}

// listPackages are the cluster's Package resources, in every namespace; a
// resource that does not decode is passed over, as the controller passes it.
func listPackages(cfg *rest.Config) []v1alpha1.Package {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterTimeout)
	defer cancel()
	list, err := client.Resource(v1alpha1.PackageGroupVersionResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var out []v1alpha1.Package
	for _, item := range list.Items {
		var p v1alpha1.Package
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}
