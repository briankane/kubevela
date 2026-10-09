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

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// MethodClusters lists the clusters joined to the cluster the kubeconfig
// names, local first.
const MethodClusters = "vela/clusters"

// ManagedCluster is a joined cluster as vela cluster list shows it: never
// its credentials.
type ManagedCluster struct {
	Name     string            `json:"name"`
	Alias    string            `json:"alias,omitempty"`
	Type     string            `json:"type"`
	Endpoint string            `json:"endpoint,omitempty"`
	Accepted bool              `json:"accepted"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// ClustersResult is the joined clusters of a context.
type ClustersResult struct {
	Context  string           `json:"context"`
	Clusters []ManagedCluster `json:"clusters"`
}

// readClusters lists the clusters joined by Secret or OCM ManagedCluster.
func readClusters(ctx context.Context, cli ctrlclient.Client) ([]ManagedCluster, error) {
	all, err := multicluster.ListVirtualClusters(ctx, cli)
	if err != nil {
		return nil, err
	}
	out := make([]ManagedCluster, 0, len(all))
	for _, vc := range all {
		m := ManagedCluster{Name: vc.Name, Alias: vc.Alias, Type: string(vc.Type), Endpoint: vc.EndPoint, Accepted: vc.Accepted}
		if vc.EndPoint == "-" {
			m.Endpoint = ""
		}
		for k, v := range vc.Labels {
			if k == "cluster.core.oam.dev/cluster-credential-type" {
				continue
			}
			if m.Labels == nil {
				m.Labels = map[string]string{}
			}
			m.Labels[k] = v
		}
		out = append(out, m)
	}
	return out, nil
}

// clustersRequest answers vela/clusters off the message loop.
func (s *Server) clustersRequest(msg message) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.clusters == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	cluster, id := s.cluster, msg.ID
	go func() {
		list, err := cluster.clusters()
		s.post(func() {
			if err != nil {
				_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: err.Error()})
				return
			}
			_ = s.reply(id, ClustersResult{Context: cluster.context, Clusters: list}, nil)
		})
	}()
	return nil
}
