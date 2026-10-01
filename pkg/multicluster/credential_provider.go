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
	"fmt"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	clustercommon "github.com/oam-dev/cluster-gateway/pkg/common"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrNoProviderTemplate means the credential provider ships no setup template.
var ErrNoProviderTemplate = errors.New("no setup template")

// ProviderSetupStep is one step a credential provider's setup template describes.
type ProviderSetupStep struct {
	Title    string   `json:"title"`
	Commands []string `json:"commands"`
}

// RenderedProvider is a credential provider's setup template rendered for one cluster.
type RenderedProvider struct {
	// ProviderConfig becomes the cluster Secret's provider-config.
	ProviderConfig []byte
	// Setup describes what to create before the cluster is joined.
	Setup []ProviderSetupStep
}

// RenderCredentialProvider renders the setup template a cluster-gateway credential
// plugin ships, a ConfigMap in the gateway's namespace, for cluster and params.
func RenderCredentialProvider(ctx context.Context, cli client.Client, provider, cluster string, params map[string]string) (*RenderedProvider, error) {
	cms := &corev1.ConfigMapList{}
	if err := cli.List(ctx, cms, client.InNamespace(ClusterGatewaySecretNamespace),
		client.MatchingLabels{clustercommon.LabelKeyCredentialProvider: provider}); err != nil {
		return nil, errors.Wrapf(err, "failed to find the setup template of credential provider %s", provider)
	}
	switch len(cms.Items) {
	case 0:
		return nil, errors.Wrapf(ErrNoProviderTemplate, "credential provider %s has no setup template in namespace %s", provider, ClusterGatewaySecretNamespace)
	case 1:
	default:
		return nil, errors.Errorf("credential provider %s has %d setup templates in namespace %s", provider, len(cms.Items), ClusterGatewaySecretNamespace)
	}
	template, ok := cms.Items[0].Data[clustercommon.CredentialProviderTemplateKey]
	if !ok {
		return nil, errors.Errorf("ConfigMap %s has no %s", cms.Items[0].Name, clustercommon.CredentialProviderTemplateKey)
	}
	rendered, err := renderProviderTemplate(template, cluster, params)
	if err != nil {
		return nil, errors.Wrapf(err, "credential provider %s", provider)
	}
	return rendered, nil
}

func renderProviderTemplate(template, cluster string, params map[string]string) (*RenderedProvider, error) {
	v := cuecontext.New().CompileString(template)
	if v.Err() != nil {
		return nil, errors.Errorf("invalid setup template: %s", cueErr(v.Err()))
	}
	accepted, err := templateParameters(v)
	if err != nil {
		return nil, err
	}
	for k := range params {
		if !accepted[k] {
			return nil, errors.Errorf("unknown parameter %q, the template accepts: %s", k, strings.Join(sortedKeys(accepted), ", "))
		}
	}

	v = v.FillPath(cue.ParsePath("context.cluster"), cluster)
	for k, val := range params {
		v = v.FillPath(cue.MakePath(cue.Str("parameter"), cue.Str(k)), val)
	}

	pc := v.LookupPath(cue.ParsePath("providerConfig"))
	if !pc.Exists() {
		return nil, errors.New("invalid setup template: no providerConfig")
	}
	if err := pc.Validate(cue.Concrete(true)); err != nil {
		return nil, errors.New(cueErr(err))
	}
	if pc.IncompleteKind() != cue.StructKind {
		return nil, errors.New("invalid setup template: providerConfig must be an object")
	}
	raw, err := pc.MarshalJSON()
	if err != nil {
		return nil, errors.New(cueErr(err))
	}
	rendered := &RenderedProvider{ProviderConfig: raw}
	if setup := v.LookupPath(cue.ParsePath("setup")); setup.Exists() {
		if err := setup.Decode(&rendered.Setup); err != nil {
			return nil, errors.New(cueErr(err))
		}
	}
	return rendered, nil
}

func templateParameters(v cue.Value) (map[string]bool, error) {
	accepted := map[string]bool{}
	p := v.LookupPath(cue.ParsePath("parameter"))
	if !p.Exists() {
		return accepted, nil
	}
	it, err := p.Fields(cue.Optional(true))
	if err != nil {
		return nil, errors.Errorf("invalid setup template: %s", cueErr(err))
	}
	for it.Next() {
		accepted[it.Selector().Unquoted()] = true
	}
	return accepted, nil
}

func cueErr(err error) string {
	return strings.TrimSpace(cueerrors.Details(err, nil))
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// String renders the steps as shell comments and commands.
func (s ProviderSetupStep) String() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "# %s\n", s.Title)
	for _, c := range s.Commands {
		fmt.Fprintln(b, c)
	}
	return b.String()
}
