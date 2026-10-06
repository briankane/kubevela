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
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// ClusterConnector reaches the cluster the kubeconfig names: whether it runs
// KubeVela, its current context's name, and how to fetch the OpenAPI v3
// document of one of its group-versions.
type ClusterConnector func() (fetch kubeschema.Fetch, hasKubeVela bool, context string, err error)

// clusterTimeout bounds each call to the cluster, so an unreachable one
// cannot hold an editor up.
const clusterTimeout = 3 * time.Second

// kubeVelaGroup is the API group a cluster running KubeVela serves.
const kubeVelaGroup = "core.oam.dev"

// connectKubeconfig is the ClusterConnector for the kubeconfig's current
// context, as kubectl reads it.
func connectKubeconfig() (kubeschema.Fetch, bool, string, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, false, "", err
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, false, raw.CurrentContext, err
	}
	cfg.Timeout = clusterTimeout
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, false, raw.CurrentContext, err
	}
	groups, err := dc.ServerGroups()
	if err != nil {
		return nil, false, raw.CurrentContext, err
	}
	vela := false
	for _, g := range groups.Groups {
		vela = vela || g.Name == kubeVelaGroup
	}
	if !vela {
		return nil, false, raw.CurrentContext, nil
	}
	paths, err := dc.OpenAPIV3().Paths()
	if err != nil {
		return nil, true, raw.CurrentContext, err
	}
	return func(gv string) ([]byte, error) {
		key := "api/" + gv
		if strings.Contains(gv, "/") {
			key = "apis/" + gv
		}
		p, ok := paths[key]
		if !ok {
			return nil, fmt.Errorf("the cluster serves no %s", gv)
		}
		return p.Schema("application/json")
	}, true, raw.CurrentContext, nil
}
