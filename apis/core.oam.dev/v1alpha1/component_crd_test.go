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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The CRD's validation runs only in an API server, so these tests install the generated CRD
// into envtest.
func TestComponentCRDValidation(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is not set: run through `make test`, or export the path from `setup-envtest use -p path`")
	}
	env := &envtest.Environment{
		CRDInstallOptions:     envtest.CRDInstallOptions{Paths: []string{filepath.Join("..", "..", "..", "charts", "vela-core", "crds", "core.oam.dev_components.yaml")}},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := context.Background()

	component := func(name string, mutate func(*ComponentSpec)) *Component {
		c := &Component{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: ComponentSpec{
				Target:    ComponentTarget{Cluster: "local"},
				Revision:  1,
				Payload:   &ComponentPayload{SecretRef: ComponentSecretReference{Name: name + "-payload"}, Format: "gzip+json/v1", SHA256: "5be0"},
				Signature: ComponentSignature{KeyID: "hub", Value: "c2ln"},
			},
		}
		if mutate != nil {
			mutate(&c.Spec)
		}
		return c
	}

	t.Run("a Component with a payload is admitted", func(t *testing.T) {
		require.NoError(t, cli.Create(ctx, component("with-payload", nil)))
	})
	t.Run("a Component without a payload is refused", func(t *testing.T) {
		err := cli.Create(ctx, component("no-payload", func(s *ComponentSpec) { s.Payload = nil }))
		require.ErrorContains(t, err, "payload is required unless delete is set")
	})
	t.Run("a tombstone needs no payload", func(t *testing.T) {
		require.NoError(t, cli.Create(ctx, component("tombstone", func(s *ComponentSpec) { s.Payload = nil; s.Delete = true })))
	})
	t.Run("revision starts at 1", func(t *testing.T) {
		err := cli.Create(ctx, component("revision-zero", func(s *ComponentSpec) { s.Revision = 0 }))
		require.ErrorContains(t, err, "spec.revision")
	})
	t.Run("a target cluster is required", func(t *testing.T) {
		err := cli.Create(ctx, component("no-cluster", func(s *ComponentSpec) { s.Target.Cluster = "" }))
		require.ErrorContains(t, err, "spec.target.cluster")
	})
	t.Run("health parameters and observations keep their fields", func(t *testing.T) {
		c := component("preserved", func(s *ComponentSpec) {
			s.Health.Workload = &ComponentHealthSubject{Parameter: &runtime.RawExtension{Raw: []byte(`{"replicas":2,"nested":{"a":1}}`)}}
			s.Observe = []ComponentObservation{{Resource: "outputs.creds", Name: "password", Path: ".data.password", Sensitive: true}}
		})
		require.NoError(t, cli.Create(ctx, c))
		c.Status = ComponentStatus{
			ObservedRevision: 1,
			Observed:         map[string]runtime.RawExtension{"output": {Raw: []byte(`{"readyReplicas":2}`)}},
			SealedObserved:   map[string]map[string]string{"outputs.creds": {"password": "c2VhbGVk"}},
			SealedWith:       "hub-1",
		}
		require.NoError(t, cli.Status().Update(ctx, c))

		got := &Component{}
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(c), got))
		require.JSONEq(t, `{"replicas":2,"nested":{"a":1}}`, string(got.Spec.Health.Workload.Parameter.Raw))
		require.True(t, got.Spec.Observe[0].Sensitive)
		require.JSONEq(t, `{"readyReplicas":2}`, string(got.Status.Observed["output"].Raw))
		require.Equal(t, "c2VhbGVk", got.Status.SealedObserved["outputs.creds"]["password"])
		require.Equal(t, "hub-1", got.Status.SealedWith)
	})
}
