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
	"path/filepath"
	"testing"
	"time"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/require"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/oam-dev/kubevela/cmd/core/app/options"
	"github.com/oam-dev/kubevela/pkg/features"
)

// A cluster the hub dispatches to has the Component and ResourceTracker CRDs and none of
// the Application ones, so vela-agent alone must start there.
func TestAgentOnlyStartsWithOnlyItsOwnCRDs(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
	crds := filepath.Join("..", "..", "..", "charts", "vela-core", "crds")
	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{
			filepath.Join(crds, "core.oam.dev_components.yaml"),
			filepath.Join(crds, "core.oam.dev_resourcetrackers.yaml"),
		}},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	coreOpts := options.NewCoreOptions()
	coreOpts.Agent.AgentOnly = true
	coreOpts.Agent.ClusterName = "spoke"
	coreOpts.Server.EnableLeaderElection = false
	coreOpts.Server.HealthAddr = "0"
	coreOpts.Observability.MetricsAddr = "0"
	coreOpts.Webhook.UseWebhook = false
	coreOpts.Webhook.CertDir = t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr, err := createControllerManager(ctx, cfg, coreOpts)
	require.NoError(t, err)
	require.NoError(t, prepareManager(ctx, mgr, coreOpts))

	started := make(chan error, 1)
	go func() { started <- mgr.Start(ctx) }()
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "the agent's caches sync without the Application CRDs")
	select {
	case err := <-started:
		t.Fatalf("manager stopped: %v", err)
	default:
	}
}

// vela-agent renders only the CUE standard library, so it never reads Package CRs, which a
// cluster the hub dispatches to need not serve.
func TestAgentOnlyLoadsNoExternalCUEPackages(t *testing.T) {
	t.Cleanup(func(was bool) func() {
		return func() { cuex.EnableExternalPackageForDefaultCompiler = was }
	}(cuex.EnableExternalPackageForDefaultCompiler))

	coreOpts := options.NewCoreOptions()
	coreOpts.CUE.EnableExternalPackage = true
	syncConfigurations(context.Background(), coreOpts)
	require.True(t, cuex.EnableExternalPackageForDefaultCompiler, "the hub keeps the flag's value")

	coreOpts.Agent.AgentOnly = true
	syncConfigurations(context.Background(), coreOpts)
	require.False(t, cuex.EnableExternalPackageForDefaultCompiler)
}
