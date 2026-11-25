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
	"encoding/json"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"sigs.k8s.io/yaml"
)

type renderedObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Webhooks []struct {
		Name  string `json:"name"`
		Rules []struct {
			Operations []string `json:"operations"`
		} `json:"rules"`
	} `json:"webhooks"`
	Spec json.RawMessage `json:"spec"`
}

// controllerArgs is vela-core's command line, as the chart renders it.
func controllerArgs(t *testing.T, objects map[string][]renderedObject) []string {
	t.Helper()
	deployments := objects["kubevela-controller.yaml Deployment"]
	require.Len(t, deployments, 1)
	var spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Args []string `json:"args"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	}
	require.NoError(t, json.Unmarshal(deployments[0].Spec, &spec))
	return spec.Template.Spec.Containers[0].Args
}

// renderChart renders the vela-core chart with values, as objects keyed "<template> <kind>".
func renderChart(t *testing.T, values map[string]interface{}) (map[string][]renderedObject, error) {
	t.Helper()
	chart, err := loader.Load("../../../charts/vela-core")
	require.NoError(t, err)
	vals, err := chartutil.ToRenderValues(chart, values,
		chartutil.ReleaseOptions{Name: "kubevela", Namespace: "vela-system", IsInstall: true}, chartutil.DefaultCapabilities)
	require.NoError(t, err)
	files, err := engine.Render(chart, vals)
	if err != nil {
		return nil, err
	}
	objects := map[string][]renderedObject{}
	for file, content := range files {
		if path.Ext(file) != ".yaml" {
			continue
		}
		for _, doc := range strings.Split(content, "\n---") {
			var o renderedObject
			require.NoError(t, yaml.Unmarshal([]byte(doc), &o), file)
			if o.Kind == "" {
				continue
			}
			key := strings.TrimPrefix(file, "vela-core/templates/") + " " + o.Kind
			objects[key] = append(objects[key], o)
		}
	}
	return objects, nil
}

func agentOnlyValues(gate bool) map[string]interface{} {
	return map[string]interface{}{
		"featureGates": map[string]interface{}{"enableComponentDispatch": gate},
		"agent":        map[string]interface{}{"only": true, "clusterName": "spoke"},
	}
}

// An agent-only install carries vela-core and the Component webhook, and none of the hub's
// webhooks, definitions or add-ons: those webhooks fail closed with nothing serving them.
func TestChartInstallsOnlyVelaAgentWhenAgentOnly(t *testing.T) {
	objects, err := renderChart(t, agentOnlyValues(true))
	require.NoError(t, err)
	var kinds []string
	for k := range objects {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	require.Equal(t, []string{
		"admission-webhooks/validatingWebhookConfiguration.yaml ValidatingWebhookConfiguration",
		"admission-webhooks/webhookService.yaml Service",
		"kubevela-controller.yaml ClusterRoleBinding",
		"kubevela-controller.yaml Deployment",
		"kubevela-controller.yaml Role",
		"kubevela-controller.yaml RoleBinding",
		"kubevela-controller.yaml ServiceAccount",
	}, kinds)

	var hooks []string
	for _, h := range objects["admission-webhooks/validatingWebhookConfiguration.yaml ValidatingWebhookConfiguration"][0].Webhooks {
		hooks = append(hooks, h.Name)
	}
	require.Equal(t, []string{"validating.core.oam-dev.v1alpha1.components"}, hooks)
	// A delete is let through: the namespace and garbage collectors must be able to make one.
	require.Equal(t, []string{"CREATE", "UPDATE"},
		objects["admission-webhooks/validatingWebhookConfiguration.yaml ValidatingWebhookConfiguration"][0].Webhooks[0].Rules[0].Operations)

	for _, arg := range controllerArgs(t, objects) {
		require.NotContains(t, arg, "cluster-gateway", "vela-agent never reaches other clusters, and there is no cluster-gateway to wait for")
		require.NotContains(t, arg, "cluster-metrics")
	}
}

// vela-core refuses --agent-only without the gate, so the chart refuses it at install instead
// of leaving the controller crash-looping.
func TestChartRefusesAgentOnlyWithoutComponentDispatch(t *testing.T) {
	_, err := renderChart(t, agentOnlyValues(false))
	require.ErrorContains(t, err, "agent.only needs featureGates.enableComponentDispatch")
}

// The hub's install is unchanged by the agent values.
func TestChartInstallsTheHubWhenNotAgentOnly(t *testing.T) {
	objects, err := renderChart(t, map[string]interface{}{"featureGates": map[string]interface{}{"enableComponentDispatch": true}})
	require.NoError(t, err)
	require.Contains(t, objects, "admission-webhooks/mutatingWebhookConfiguration.yaml MutatingWebhookConfiguration")
	require.Contains(t, objects, "builtin-definitions/jobs.yaml Job")
	require.Contains(t, objects, "velaql/resourceTree.yaml ConfigMap")
	require.Greater(t, len(objects["admission-webhooks/validatingWebhookConfiguration.yaml ValidatingWebhookConfiguration"][0].Webhooks), 1)
	require.Contains(t, controllerArgs(t, objects), "--enable-cluster-gateway")
}

// The hub rotates its signing key on a schedule only when a maximum age is set.
func TestChartSetsTheSigningKeyMaxAge(t *testing.T) {
	gate := map[string]interface{}{"enableComponentDispatch": true}
	objects, err := renderChart(t, map[string]interface{}{"featureGates": gate})
	require.NoError(t, err)
	for _, arg := range controllerArgs(t, objects) {
		require.NotContains(t, arg, "--component-signing-key-max-age")
	}

	objects, err = renderChart(t, map[string]interface{}{"featureGates": gate,
		"componentDispatch": map[string]interface{}{"signingKeyMaxAge": "2160h"}})
	require.NoError(t, err)
	require.Contains(t, controllerArgs(t, objects), "--component-signing-key-max-age=2160h")
}
