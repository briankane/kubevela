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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// The credential plugin itself is named with FlagProvider.
const (
	// FlagProviderParam is a key=value parameter of the provider's setup template
	FlagProviderParam = "param"
	// FlagProviderConfig is the JSON configuration passed to the credential plugin as is
	FlagProviderConfig = "provider-config"
)

// parseParams turns repeated key=value flags into a map.
func parseParams(pairs []string) (map[string]string, error) {
	params := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, errors.Errorf("--%s %q: want key=value", FlagProviderParam, p)
		}
		if _, dup := params[k]; dup {
			return nil, errors.Errorf("--%s %s given twice", FlagProviderParam, k)
		}
		params[k] = v
	}
	return params, nil
}

// resolveProviderConfig returns the provider-config for a cluster: rawConfig as
// given, or the provider's setup template rendered with params. A provider
// without a template, given neither, gets no configuration.
func resolveProviderConfig(ctx context.Context, cli client.Client, provider, cluster, rawConfig string, params map[string]string) ([]byte, error) {
	if rawConfig != "" {
		if len(params) > 0 {
			return nil, errors.Errorf("use --%s or --%s, not both", FlagProviderConfig, FlagProviderParam)
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(rawConfig), &obj); err != nil {
			return nil, errors.Errorf("--%s must be a JSON object: %v", FlagProviderConfig, err)
		}
		return []byte(rawConfig), nil
	}
	rendered, err := multicluster.RenderCredentialProvider(ctx, cli, provider, cluster, params)
	if errors.Is(err, multicluster.ErrNoProviderTemplate) && len(params) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rendered.ProviderConfig, nil
}

// storedExecTokenWarning explains that join ran the kubeconfig's exec plugin once
// and stored its token, which stops working when the token expires.
func storedExecTokenWarning(cfg *multicluster.KubeClusterConfig) string {
	if cfg.CredentialProvider != nil || cfg.AuthInfo == nil || cfg.AuthInfo.Exec == nil {
		return ""
	}
	return fmt.Sprintf("Warning: the kubeconfig's exec plugin (%s) was run once and its token stored. "+
		"Short-lived tokens, such as EKS's 15 minutes, stop working when they expire; "+
		"register the cluster with --%s so cluster-gateway mints them itself.\n", cfg.AuthInfo.Exec.Command, FlagProvider)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeProviderSetup prints the provider's setup steps for one cluster, then the
// join command that registers it with the same parameters.
func writeProviderSetup(w io.Writer, cluster, provider string, params map[string]string, rendered *multicluster.RenderedProvider) {
	for _, step := range rendered.Setup {
		_, _ = fmt.Fprintln(w, step.String())
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	join := []string{"vela cluster join <kubeconfig> --name", shellQuote(cluster), "--" + FlagProvider, shellQuote(provider)}
	for _, k := range keys {
		join = append(join, "--"+FlagProviderParam, shellQuote(k+"="+params[k]))
	}
	_, _ = fmt.Fprintf(w, "# Then register the cluster:\n%s\n", strings.Join(join, " "))
}

// NewClusterSetupCommand prints what a credential provider needs set up for a
// cluster before it is joined, from the setup template the provider ships.
func NewClusterSetupCommand(c *common.Args) *cobra.Command {
	var provider string
	var paramPairs []string
	cmd := &cobra.Command{
		Use:   "setup CLUSTER",
		Short: "Print what a credential provider needs set up before a cluster is joined.",
		Long: "Render the setup template a cluster-gateway credential plugin ships for one cluster: the cloud " +
			"resources to create beforehand, and the vela cluster join command that registers the cluster. " +
			"Nothing is run.",
		Example: "> vela cluster setup prod-eu --provider aws --param region=eu-west-1 \\\n" +
			"    --param accountId=222222222222 --param hubRoleArn=arn:aws:iam::111111111111:role/vela-cluster-gateway",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" {
				return errors.Errorf("--%s is required", FlagProvider)
			}
			params, err := parseParams(paramPairs)
			if err != nil {
				return err
			}
			cli, err := c.GetClient()
			if err != nil {
				return err
			}
			rendered, err := multicluster.RenderCredentialProvider(cmd.Context(), cli, provider, args[0], params)
			if err != nil {
				return err
			}
			writeProviderSetup(cmd.OutOrStdout(), args[0], provider, params, rendered)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, FlagProvider, "", "credential provider, e.g. aws")
	cmd.Flags().StringArrayVar(&paramPairs, FlagProviderParam, nil, "key=value parameter of the provider's setup template, repeatable")
	return cmd
}
