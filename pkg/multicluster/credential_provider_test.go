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

package multicluster

import (
	"context"
	"os"
	"testing"

	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testProviderTemplate = `
context: cluster: string
parameter: {
	region:  =~"^[a-z]+-[a-z]+-[0-9]$"
	role:    *"role-\(context.cluster)" | string
	policy?: string
}
providerConfig: {
	region: parameter.region
	roleArn: "arn:aws:iam::1:role/\(parameter.role)"
}
setup: [{
	title: "Create \(parameter.role)"
	commands: ["create \(parameter.role)", if parameter.policy != _|_ {"attach \(parameter.policy)"}]
}]
`

func providerTemplateConfigMap(provider, template string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "credential-provider-" + provider,
			Namespace: ClusterGatewaySecretNamespace,
			Labels:    map[string]string{clustercommon.LabelKeyCredentialProvider: provider},
		},
		Data: map[string]string{clustercommon.CredentialProviderTemplateKey: template},
	}
}

func TestRenderCredentialProvider(t *testing.T) {
	ctx := context.Background()
	ClusterGatewaySecretNamespace = "vela-system"
	cli := fake.NewClientBuilder().WithScheme(newTestScheme()).
		WithObjects(providerTemplateConfigMap("aws", testProviderTemplate)).Build()

	rendered, err := RenderCredentialProvider(ctx, cli, "aws", "prod", map[string]string{"region": "eu-west-1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"region":"eu-west-1","roleArn":"arn:aws:iam::1:role/role-prod"}`, string(rendered.ProviderConfig))
	require.Equal(t, []ProviderSetupStep{{Title: "Create role-prod", Commands: []string{"create role-prod"}}}, rendered.Setup)

	rendered, err = RenderCredentialProvider(ctx, cli, "aws", "prod", map[string]string{"region": "eu-west-1", "role": "custom", "policy": "edit"})
	require.NoError(t, err)
	require.Equal(t, []string{"create custom", "attach edit"}, rendered.Setup[0].Commands)

	_, err = RenderCredentialProvider(ctx, cli, "gcp", "prod", nil)
	require.ErrorIs(t, err, ErrNoProviderTemplate)

	_, err = RenderCredentialProvider(ctx, cli, "aws", "prod", map[string]string{"region": "eu-west-1", "regoin": "x"})
	require.ErrorContains(t, err, `unknown parameter "regoin"`)
	require.ErrorContains(t, err, "policy, region, role")

	_, err = RenderCredentialProvider(ctx, cli, "aws", "prod", nil)
	require.ErrorContains(t, err, "region")

	_, err = RenderCredentialProvider(ctx, cli, "aws", "prod", map[string]string{"region": "EU WEST"})
	require.ErrorContains(t, err, "region")
}

func TestRenderCredentialProviderTemplateErrors(t *testing.T) {
	ctx := context.Background()
	ClusterGatewaySecretNamespace = "vela-system"
	cases := map[string]string{
		"does not compile":         `parameter: {`,
		"providerConfig missing":   `context: cluster: string` + "\nparameter: {}",
		"providerConfig not a map": `context: cluster: string` + "\nparameter: {}\nproviderConfig: \"x\"",
	}
	for name, template := range cases {
		t.Run(name, func(t *testing.T) {
			cli := fake.NewClientBuilder().WithScheme(newTestScheme()).
				WithObjects(providerTemplateConfigMap("bad", template)).Build()
			_, err := RenderCredentialProvider(ctx, cli, "bad", "prod", nil)
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrNoProviderTemplate)
		})
	}
}

// The AWS plugin's template, as the vela-core chart ships it, renders with this
// package's CUE. Its agreement with the plugin is tested in cluster-gateway.
func TestRenderShippedAWSTemplate(t *testing.T) {
	template, err := os.ReadFile("../../charts/vela-core/credential-providers/aws.cue")
	require.NoError(t, err)
	rendered, err := renderProviderTemplate(string(template), "prod-eu", map[string]string{
		"region":       "eu-west-1",
		"accountId":    "222222222222",
		"hubRoleArn":   "arn:aws:iam::111111111111:role/vela-cluster-gateway",
		"accessPolicy": "AmazonEKSEditPolicy",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"region":"eu-west-1","clusterName":"prod-eu","roleArn":"arn:aws:iam::222222222222:role/vela-prod-eu"}`, string(rendered.ProviderConfig))
	require.NotEmpty(t, rendered.Setup)
	require.Contains(t, rendered.Setup[0].Commands[0], `"sts:RoleSessionName":"cluster-gateway-prod-eu"`)
}
