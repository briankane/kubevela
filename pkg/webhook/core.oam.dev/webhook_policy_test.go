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

package core_oam_dev

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"

	"github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/writer"
)

// webhookPolicies renders the chart's webhook configurations with values and returns each
// webhook's failurePolicy by name.
func webhookPolicies(t *testing.T, values map[string]interface{}) map[string]string {
	t.Helper()
	chart, err := loader.Load("../../../charts/vela-core")
	require.NoError(t, err)
	vals, err := chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{Name: "kubevela", Namespace: "vela-system", IsInstall: true}, chartutil.DefaultCapabilities)
	require.NoError(t, err)
	out, err := engine.Render(chart, vals)
	require.NoError(t, err)
	policies := map[string]string{}
	re := regexp.MustCompile(`(?m)^\s*failurePolicy:\s*(\S+)\s*\n\s*name:\s*(\S+)`)
	for name, doc := range out {
		if !strings.Contains(name, "admission-webhooks/") || !strings.Contains(name, "WebhookConfiguration") {
			continue
		}
		require.Equal(t, strings.Count(doc, "failurePolicy:"), len(re.FindAllStringSubmatch(doc, -1)),
			"%s: every failurePolicy is followed by its webhook's name", name)
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			policies[m[2]] = m[1]
		}
	}
	require.NotEmpty(t, policies, "the chart renders webhook configurations")
	return policies
}

func webhooks(certManager, patch bool) map[string]interface{} {
	return map[string]interface{}{"admissionWebhooks": map[string]interface{}{
		"certManager": map[string]interface{}{"enabled": certManager},
		"patch":       map[string]interface{}{"enabled": patch},
	}}
}

// The chart renders the configured failure policy wherever nothing else would set it. Webhooks
// start as Ignore only where vela-core manages the certificate and sets the policy itself once it
// serves; with cert-manager or a supplied certificate the chart's Fail is what applies. The writer
// stamp is Ignore in every mode.
func TestWebhooksRenderTheConfiguredFailurePolicyPerCertificateMode(t *testing.T) {
	expect := func(values map[string]interface{}, want, why string) {
		policies := webhookPolicies(t, values)
		require.Equal(t, "Ignore", policies[writer.WebhookName], "the writer stamp fails open")
		for name, p := range policies {
			if name != writer.WebhookName {
				require.Equal(t, want, p, "%s: %s", name, why)
			}
		}
	}
	expect(webhooks(false, true), "Ignore", "vela-core manages the certificate: it sets the policy once it serves")
	expect(webhooks(true, true), "Fail", "cert-manager: nothing else sets the policy")
	expect(webhooks(true, false), "Fail", "cert-manager")
	expect(webhooks(false, false), "Fail", "a supplied certificate")
}
