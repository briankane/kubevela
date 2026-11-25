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

package app

import (
	"context"

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/cmd/core/app/options"
	"github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1alpha1/component"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	componentwebhook "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1alpha1/component"
)

// setupAgent registers vela-agent: the Component reconciler, trusting the keys in this
// cluster's trust Secret, and with webhooks on, the admission of Components from their
// producers only. Keys are read live, so a rotation the hub pushes applies at once.
func setupAgent(manager ctrl.Manager, coreOptions *options.CoreOptions) error {
	if coreOptions.Webhook.UseWebhook {
		producers, err := componentwebhook.ParseProducers(coreOptions.Agent.Producers)
		if err != nil {
			return err
		}
		klog.InfoS("Registering the Component admission webhook", "producers", producers.String())
		componentwebhook.RegisterValidatingHandler(manager, producers)
	}
	namespace := resolveControllerNamespace(coreOptions.Server.LeaderElectionNamespace)
	reconciler := &component.Reconciler{
		Client:         manager.GetClient(),
		Keys:           component.SecretKeySource{Reader: manager.GetAPIReader(), Namespace: namespace},
		TrustNamespace: namespace,
		Cluster:        coreOptions.Agent.ClusterName,
	}
	return reconciler.SetupWithManager(manager)
}

// setupComponentDispatch registers what the hub runs for Component dispatch: vela-agent for
// the hub's own cluster, and the push that keeps the hub's keys trusted on every cluster.
func setupComponentDispatch(manager ctrl.Manager, coreOptions *options.CoreOptions) error {
	if err := setupAgent(manager, coreOptions); err != nil {
		return err
	}
	klog.InfoS("Setting up the component signing key push")
	clusterReader, err := multicluster.NewClusterReader(manager.GetConfig(), manager.GetRESTMapper())
	if err != nil {
		return err
	}
	return manager.Add(&component.TrustPusher{
		Client:    manager.GetClient(),
		Reader:    clusterReader,
		Namespace: resolveControllerNamespace(coreOptions.Server.LeaderElectionNamespace),
		Clusters:  joinedClusters(manager.GetClient()),
		Recorder:  manager.GetEventRecorderFor("vela-core"), //nolint:staticcheck // the application controller records with the same API
		MaxKeyAge: coreOptions.Agent.SigningKeyMaxAge,
	})
}

// joinedClusters lists the clusters registered with cluster-gateway, without the local one.
func joinedClusters(cli ctrlclient.Client) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		clusters, err := multicluster.FindVirtualClustersByLabels(ctx, cli, map[string]string{})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(clusters))
		for _, c := range clusters {
			if c.Name != multicluster.ClusterLocalName {
				names = append(names, c.Name)
			}
		}
		return names, nil
	}
}
