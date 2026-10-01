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

package cli

import (
	"bytes"
	"context"
	"testing"

	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/pkg/multicluster"
)

const testTemplate = `
context: cluster: string
parameter: {
	region: string
	role:   *"role-\(context.cluster)" | string
}
providerConfig: {region: parameter.region, role: parameter.role}
setup: [{title: "Create \(parameter.role)", commands: ["create \(parameter.role) --region \(parameter.region)"]}]
`

func clientWithTemplate(t *testing.T, provider, template string) client.Client {
	t.Helper()
	multicluster.ClusterGatewaySecretNamespace = "vela-system"
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	b := fake.NewClientBuilder().WithScheme(s)
	if template != "" {
		b = b.WithObjects(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "credential-provider-" + provider, Namespace: "vela-system",
				Labels: map[string]string{clustercommon.LabelKeyCredentialProvider: provider},
			},
			Data: map[string]string{clustercommon.CredentialProviderTemplateKey: template},
		})
	}
	return b.Build()
}

func TestParseParams(t *testing.T) {
	params, err := parseParams([]string{"region=eu-west-1", "note=a=b"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"region": "eu-west-1", "note": "a=b"}, params)

	for _, bad := range [][]string{{"region"}, {"=x"}, {"a=1", "a=2"}} {
		_, err := parseParams(bad)
		require.Error(t, err, "%v", bad)
	}
}

func TestResolveProviderConfig(t *testing.T) {
	ctx := context.Background()
	withTemplate := clientWithTemplate(t, "acme", testTemplate)
	noTemplate := clientWithTemplate(t, "acme", "")

	cfg, err := resolveProviderConfig(ctx, withTemplate, "acme", "prod", "", map[string]string{"region": "eu-west-1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"region":"eu-west-1","role":"role-prod"}`, string(cfg))

	_, err = resolveProviderConfig(ctx, withTemplate, "acme", "prod", "", nil)
	require.ErrorContains(t, err, "region", "template parameters without defaults are required")

	cfg, err = resolveProviderConfig(ctx, withTemplate, "acme", "prod", `{"raw":true}`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"raw":true}`, string(cfg), "an explicit provider-config bypasses the template")

	_, err = resolveProviderConfig(ctx, withTemplate, "acme", "prod", `{"raw":true}`, map[string]string{"region": "x"})
	require.ErrorContains(t, err, "--param")

	_, err = resolveProviderConfig(ctx, withTemplate, "acme", "prod", `nope`, nil)
	require.ErrorContains(t, err, "--provider-config")

	cfg, err = resolveProviderConfig(ctx, noTemplate, "acme", "prod", "", nil)
	require.NoError(t, err)
	require.Nil(t, cfg, "a plugin without a template may need no configuration")

	_, err = resolveProviderConfig(ctx, noTemplate, "acme", "prod", "", map[string]string{"region": "x"})
	require.ErrorContains(t, err, "no setup template")
}

func TestWriteProviderSetup(t *testing.T) {
	rendered, err := multicluster.RenderCredentialProvider(context.Background(), clientWithTemplate(t, "acme", testTemplate),
		"acme", "prod", map[string]string{"region": "eu-west-1", "role": "it's mine"})
	require.NoError(t, err)

	var out bytes.Buffer
	writeProviderSetup(&out, "prod", "acme", map[string]string{"region": "eu-west-1", "role": "it's mine"}, rendered)
	require.Equal(t, `# Create it's mine
create it's mine --region eu-west-1

# Then register the cluster:
vela cluster join <kubeconfig> --name prod --provider acme --param region=eu-west-1 --param 'role=it'\''s mine'
`, out.String())
}

func TestStoredExecTokenWarning(t *testing.T) {
	withExec := &multicluster.KubeClusterConfig{AuthInfo: &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "aws"}}}
	require.Contains(t, storedExecTokenWarning(withExec), "--provider")

	withProvider := &multicluster.KubeClusterConfig{
		AuthInfo:           &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "aws"}},
		CredentialProvider: &multicluster.CredentialProvider{Name: "aws"},
	}
	require.Empty(t, storedExecTokenWarning(withProvider))

	withToken := &multicluster.KubeClusterConfig{AuthInfo: &clientcmdapi.AuthInfo{Token: "t"}}
	require.Empty(t, storedExecTokenWarning(withToken))
}
