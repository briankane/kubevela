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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apicommon "github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

func addonApp(name, version string, phase apicommon.ApplicationPhase, healthy bool) *v1beta1.Application {
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: "addon-" + name, Namespace: "vela-system", Labels: map[string]string{oam.LabelAddonName: name, oam.LabelAddonVersion: version, oam.LabelAddonRegistry: "KubeVela"}}}
	app.Status.Phase = phase
	app.Status.Services = []apicommon.ApplicationComponentStatus{{Name: name, Healthy: healthy}}
	return app
}

func TestReadAddons(t *testing.T) {
	registries := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "vela-addon-registry", Namespace: "vela-system"}, Data: map[string]string{
		"registries": `{"KubeVela":{"name":"KubeVela","helm":{"url":"https://kubevela.github.io/catalog/official"}},"mine":{"name":"mine","helm":{"url":"oci://ghcr.io/me/addons"}}}`,
	}}
	other := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "vela-system"}}
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).WithObjects(
		addonApp("velaux", "1.12.0", apicommon.ApplicationRunning, true),
		addonApp("fluxcd", "2.3.0", apicommon.ApplicationRunning, false),
		other, registries,
	).WithStatusSubresource(&v1beta1.Application{}).Build()
	r, err := readAddons(context.Background(), cli)
	require.NoError(t, err)
	assert.Equal(t, []EnabledAddon{
		{Name: "fluxcd", Version: "2.3.0", Registry: "KubeVela", Phase: "running", Healthy: false},
		{Name: "velaux", Version: "1.12.0", Registry: "KubeVela", Phase: "running", Healthy: true},
	}, r.Enabled, "an Application not of an addon is left out; one with an unhealthy service is not healthy")
	assert.Equal(t, []AddonRegistry{
		{Name: "KubeVela", Type: "helm", URL: "https://kubevela.github.io/catalog/official"},
		{Name: "mine", Type: "oci", URL: "oci://ghcr.io/me/addons"},
	}, r.Registries)

	_, err = readRegistryAddons(context.Background(), cli, "nope")
	assert.ErrorContains(t, err, "no addon registry nope")
}
