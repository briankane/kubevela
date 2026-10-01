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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/oam-dev/kubevela/pkg/multicluster"
)

func TestStoredExecTokenWarning(t *testing.T) {
	withExec := &multicluster.KubeClusterConfig{AuthInfo: &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "aws"}}}
	require.Contains(t, storedExecTokenWarning(withExec), "--credential-provider")

	withProvider := &multicluster.KubeClusterConfig{
		AuthInfo:           &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "aws"}},
		CredentialProvider: &multicluster.CredentialProvider{Name: "aws"},
	}
	require.Empty(t, storedExecTokenWarning(withProvider))

	withToken := &multicluster.KubeClusterConfig{AuthInfo: &clientcmdapi.AuthInfo{Token: "t"}}
	require.Empty(t, storedExecTokenWarning(withToken))
}

func TestCredentialProviderOption(t *testing.T) {
	op, err := credentialProviderOption("", `{"region":"eu-west-1"}`)
	require.NoError(t, err)
	require.Nil(t, op)

	op, err = credentialProviderOption("aws", `{"region":"eu-west-1"}`)
	require.NoError(t, err)
	require.Equal(t, multicluster.JoinClusterCredentialProviderOption{Name: "aws", Config: []byte(`{"region":"eu-west-1"}`)}, *op)

	_, err = credentialProviderOption("", `{"region":"eu-west-1"}x`)
	require.NoError(t, err, "config is ignored without a provider")
	_, err = credentialProviderOption("aws", `nope`)
	require.ErrorContains(t, err, "--provider-config")
}

func TestEKSSetup(t *testing.T) {
	opts := eksSetupOptions{
		Cluster:         "prod-eu",
		Region:          "eu-west-1",
		EKSCluster:      "prod-eu-1",
		AccountID:       "222222222222",
		HubRoleARN:      "arn:aws:iam::111111111111:role/vela-cluster-gateway",
		RoleName:        "vela-{cluster}",
		SessionPrefix:   "cluster-gateway",
		KubernetesGroup: "vela:cluster-gateway",
	}
	var out bytes.Buffer
	require.NoError(t, writeEKSSetup(&out, opts))
	script := out.String()

	require.Contains(t, script, "aws iam create-role --role-name vela-prod-eu ")
	require.Contains(t, script, "aws eks create-access-entry --region eu-west-1 --cluster-name prod-eu-1 --principal-arn arn:aws:iam::222222222222:role/vela-prod-eu --kubernetes-groups vela:cluster-gateway")
	require.Contains(t, script, "vela cluster join <kubeconfig> --name prod-eu --credential-provider aws")

	// The trust policy and provider-config are embedded as single-quoted JSON.
	trust := between(t, script, "--assume-role-policy-document '", "'")
	var policy struct {
		Statement []struct {
			Principal map[string]string
			Action    string
			Condition map[string]map[string]string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(trust), &policy))
	require.Equal(t, "arn:aws:iam::111111111111:role/vela-cluster-gateway", policy.Statement[0].Principal["AWS"])
	require.Equal(t, "sts:AssumeRole", policy.Statement[0].Action)
	require.Equal(t, "cluster-gateway-prod-eu", policy.Statement[0].Condition["StringEquals"]["sts:RoleSessionName"])

	providerConfig := between(t, script, "--provider-config '", "'")
	require.JSONEq(t, `{"region":"eu-west-1","clusterName":"prod-eu-1","roleArn":"arn:aws:iam::222222222222:role/vela-prod-eu"}`, providerConfig)
}

func TestEKSSetupRejects(t *testing.T) {
	valid := eksSetupOptions{
		Cluster: "prod", Region: "eu-west-1", EKSCluster: "prod", AccountID: "222222222222",
		HubRoleARN: "arn:aws:iam::111111111111:role/hub", RoleName: "vela-{cluster}",
		SessionPrefix: "cluster-gateway", KubernetesGroup: "vela:cluster-gateway",
	}
	cases := map[string]func(o *eksSetupOptions){
		"cluster":       func(o *eksSetupOptions) { o.Cluster = "" },
		"region":        func(o *eksSetupOptions) { o.Region = "" },
		"account-id":    func(o *eksSetupOptions) { o.AccountID = "12345" },
		"hub-role-arn":  func(o *eksSetupOptions) { o.HubRoleARN = "hub" },
		"role name":     func(o *eksSetupOptions) { o.RoleName = "vela/{cluster}" },
		"64 characters": func(o *eksSetupOptions) { o.Cluster = strings.Repeat("a", 50) },
	}
	for want, mutate := range cases {
		t.Run(want, func(t *testing.T) {
			o := valid
			mutate(&o)
			require.ErrorContains(t, writeEKSSetup(&bytes.Buffer{}, o), want)
		})
	}
}

func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	require.GreaterOrEqual(t, i, 0, "missing %q", start)
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	require.GreaterOrEqual(t, j, 0, "missing %q after %q", end, start)
	return rest[:j]
}
