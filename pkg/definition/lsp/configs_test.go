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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1alpha1 "github.com/oam-dev/kubevela/apis/config.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/config"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// sensitiveTemplateCUE is a config template whose values are never read back.
const sensitiveTemplateCUE = `
metadata: {
	name:      "api-token"
	scope:     "system"
	sensitive: true
}
template: {
	output: {
		apiVersion: "v1"
		kind:       "Secret"
		metadata: {name: context.name, namespace: context.namespace}
		stringData: token: parameter.token
	}
	parameter: {
		// +usage=The token
		token: string
	}
}
`

// configCluster is a cluster holding config templates and configs of both
// storages: a legacy image-registry template and config, and a CRD api-token
// template with a Config whose values are in a Secret.
func configCluster(t *testing.T) ctrlclient.Client {
	t.Helper()
	ctx := context.Background()
	cli := fake.NewClientBuilder().WithScheme(common.Scheme).Build()
	f := config.NewConfigFactory(cli)

	src, err := os.ReadFile(filepath.Join("..", "..", "..", "references", "cli", "test-data", "config-templates", "image-registry.cue"))
	require.NoError(t, err)
	registry, err := f.ParseTemplate(ctx, "", src)
	require.NoError(t, err)
	require.NoError(t, f.CreateOrUpdateConfigTemplate(ctx, types.DefaultKubeVelaNS, registry))
	ui := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "config-uischema-image-registry", Namespace: types.DefaultKubeVelaNS},
		Data:       map[string]string{types.UISchema: "- jsonKey: registry\n  label: Registry\n"},
	}
	require.NoError(t, cli.Create(ctx, ui))
	cfg, err := f.ParseConfig(ctx, config.NamespacedName{Name: "image-registry", Namespace: types.DefaultKubeVelaNS},
		config.Metadata{NamespacedName: config.NamespacedName{Name: "ghcr", Namespace: "shop"}, Alias: "GitHub", Properties: map[string]interface{}{"registry": "ghcr.io"}})
	require.NoError(t, err)
	require.NoError(t, f.CreateOrUpdateConfig(ctx, cfg, "shop"))

	require.NoError(t, cli.Create(ctx, &configv1alpha1.ConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "api-token", Namespace: types.DefaultKubeVelaNS},
		Spec:       configv1alpha1.ConfigTemplateSpec{Template: sensitiveTemplateCUE, Scope: configv1alpha1.ConfigTemplateScopeSystem, Sensitive: true, Description: "A token"},
	}))
	require.NoError(t, cli.Create(ctx, &configv1alpha1.Config{
		ObjectMeta: metav1.ObjectMeta{Name: "ci-token", Namespace: types.DefaultKubeVelaNS},
		Spec: configv1alpha1.ConfigSpec{
			TemplateRef:    &configv1alpha1.ConfigTemplateReference{Name: "api-token"},
			PropertiesFrom: &configv1alpha1.PropertiesReference{SecretRef: configv1alpha1.SecretKeySelector{Name: "ci-token-properties"}},
		},
		Status: configv1alpha1.ConfigStatus{Phase: configv1alpha1.ConfigPhaseAvailable},
	}))
	require.NoError(t, cli.Create(ctx, &configv1alpha1.ConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "source-infra-1a2b", Namespace: types.DefaultKubeVelaNS, Labels: map[string]string{types.LabelSourceDefinitionName: "infra"}},
		Spec:       configv1alpha1.ConfigTemplateSpec{Template: sensitiveTemplateCUE, Scope: configv1alpha1.ConfigTemplateScopeSystem},
	}))
	require.NoError(t, cli.Create(ctx, &configv1alpha1.Config{
		ObjectMeta: metav1.ObjectMeta{Name: "docker-hub", Namespace: types.DefaultKubeVelaNS},
		Spec: configv1alpha1.ConfigSpec{
			TemplateRef: &configv1alpha1.ConfigTemplateReference{Name: "image-registry"},
			Properties:  &runtime.RawExtension{Raw: []byte(`{"registry":"index.docker.io"}`)},
		},
	}))
	return cli
}

// Templates and configs are listed from both storages, each saying which.
func TestReadConfigs(t *testing.T) {
	out, err := readConfigs(context.Background(), configCluster(t))
	require.NoError(t, err)

	byName := map[string]ConfigTemplateInfo{}
	for _, tm := range out.Templates {
		byName[tm.Name] = tm
	}
	// ParseTemplate leaves metadata.description out of what it writes.
	assert.Equal(t, ConfigTemplateInfo{Name: "image-registry", Namespace: "vela-system", Alias: "Image Registry", Scope: "project", Stored: storedConfigMap}, byName["image-registry"])
	assert.Equal(t, ConfigTemplateInfo{Name: "api-token", Namespace: "vela-system", Description: "A token", Scope: "system", Sensitive: true, Stored: storedResource}, byName["api-token"])
	// A SourceDefinition's template is listed, naming it: its configs are the source's to write.
	assert.Equal(t, ConfigTemplateInfo{Name: "source-infra-1a2b", Namespace: "vela-system", Scope: "system", Stored: storedResource, Source: "infra"}, byName["source-infra-1a2b"])

	configs := map[string]ConfigInfo{}
	for _, c := range out.Configs {
		configs[c.Namespace+"/"+c.Name] = c
	}
	require.Len(t, configs, 3)
	assert.Equal(t, "image-registry", configs["shop/ghcr"].Template)
	assert.Equal(t, "vela-system", configs["shop/ghcr"].TemplateNamespace)
	assert.Equal(t, "GitHub", configs["shop/ghcr"].Alias)
	assert.Equal(t, storedSecret, configs["shop/ghcr"].Stored)
	assert.Equal(t, "api-token", configs["vela-system/ci-token"].Template)
	assert.Equal(t, "vela-system", configs["vela-system/ci-token"].TemplateNamespace, "a template reference without a namespace is to vela-system")
	assert.Equal(t, "Available", configs["vela-system/ci-token"].Phase)
	assert.Equal(t, storedResource, configs["vela-system/ci-token"].Stored)
}

// A template's form comes from its parameter, and its UI schema when it has one.
func TestReadConfigTemplate(t *testing.T) {
	cli := configCluster(t)
	out, err := readConfigTemplate(context.Background(), cli, "vela-system", "image-registry")
	require.NoError(t, err)
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(out.Schema, &schema))
	assert.Equal(t, []string{"registry"}, schema.Required)
	assert.Contains(t, schema.Properties, "auth")
	assert.JSONEq(t, `[{"jsonKey":"registry","label":"Registry"}]`, string(out.UISchema))
	assert.False(t, out.Sensitive)

	out, err = readConfigTemplate(context.Background(), cli, "vela-system", "api-token")
	require.NoError(t, err)
	assert.True(t, out.Sensitive)
	assert.Equal(t, "null", string(out.UISchema))
	assert.Contains(t, string(out.Schema), `"token"`)

	_, err = readConfigTemplate(context.Background(), cli, "vela-system", "missing")
	assert.Error(t, err)
}

// A config's values are read back, except a sensitive one's, which says why not.
func TestReadConfigProperties(t *testing.T) {
	cli := configCluster(t)
	ctx := context.Background()

	out, err := readConfigProperties(ctx, cli, "shop", "ghcr")
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"registry": "ghcr.io"}, out.Properties)
	assert.Empty(t, out.Hidden)

	out, err = readConfigProperties(ctx, cli, "vela-system", "docker-hub")
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"registry": "index.docker.io"}, out.Properties)

	out, err = readConfigProperties(ctx, cli, "vela-system", "ci-token")
	require.NoError(t, err)
	assert.Nil(t, out.Properties)
	assert.Equal(t, "its template api-token is sensitive, so its values are never read back", out.Hidden)

	_, err = readConfigProperties(ctx, cli, "shop", "missing")
	assert.Error(t, err)
}

// The config requests answer from the cluster, and refuse without one.
func TestConfigRequests(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Configs = func() (ConfigsResult, error) {
			return ConfigsResult{Templates: []ConfigTemplateInfo{{Name: "image-registry"}}, Configs: []ConfigInfo{}}, nil
		}
		c.ConfigTemplate = func(namespace, name string) (ConfigTemplateResult, error) {
			return ConfigTemplateResult{Name: name, Namespace: namespace, Schema: json.RawMessage(`{}`), UISchema: json.RawMessage("null")}, nil
		}
		c.ConfigProperties = func(namespace, name string) (ConfigPropertiesResult, error) {
			return ConfigPropertiesResult{Properties: map[string]interface{}{"registry": namespace + "/" + name}}, nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	var list ConfigsResult
	require.NoError(t, json.Unmarshal(c.response(c.send(MethodConfigs, struct{}{}, true))["result"], &list))
	assert.Equal(t, "k3d-test", list.Context)
	assert.Equal(t, "image-registry", list.Templates[0].Name)

	var tmpl ConfigTemplateResult
	require.NoError(t, json.Unmarshal(c.response(c.send(MethodConfigTemplate, ConfigParams{Namespace: "vela-system", Name: "image-registry"}, true))["result"], &tmpl))
	assert.Equal(t, "image-registry", tmpl.Name)
	assert.Equal(t, "k3d-test", tmpl.Context)

	var props ConfigPropertiesResult
	require.NoError(t, json.Unmarshal(c.response(c.send(MethodConfigProperties, ConfigParams{Namespace: "shop", Name: "ghcr"}, true))["result"], &props))
	assert.Equal(t, "shop/ghcr", props.Properties["registry"])

	off := newClientWith(t, NewServer())
	off.drain()
	off.response(off.send("initialize", map[string]interface{}{}, true))
	m := off.response(off.send(MethodConfigs, struct{}{}, true))
	assert.Contains(t, string(m["error"]), "the cluster was not reached")
}

// A config template anywhere in the workspace is checked as one.
func TestServerChecksConfigTemplates(t *testing.T) {
	c := newClient(t)
	text := strings.Replace(sensitiveTemplateCUE, `"api-token"`, `"API_Token"`, 1)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: "file:///x/token.cue", LanguageID: "cue", Version: 1, Text: text,
	}}, false)
	p := c.diagnostics()
	require.Len(t, p.Diagnostics, 1)
	assert.Contains(t, p.Diagnostics[0].Message, "metadata.name names the template's ConfigMap or ConfigTemplate")
	assert.EqualValues(t, 2, p.Diagnostics[0].Range.Start.Line)
}

// Every template's source is read for checking Configs, from both storages.
func TestReadConfigTemplateSources(t *testing.T) {
	out, err := readConfigTemplateSources(context.Background(), configCluster(t))
	require.NoError(t, err)
	byName := map[string]analysis.ConfigTemplate{}
	for _, tm := range out {
		byName[tm.Name] = tm
	}
	require.Contains(t, byName, "image-registry")
	assert.Contains(t, byName["image-registry"].CUE, "parameter")
	assert.False(t, byName["image-registry"].Sensitive)
	assert.True(t, byName["api-token"].Sensitive)
	assert.Equal(t, "the cluster", byName["api-token"].Where)
}

// A Config is checked against the template it names, the workspace's or the cluster's.
func TestServerChecksConfigs(t *testing.T) {
	root := t.TempDir()
	registry := strings.Replace(strings.Replace(sensitiveTemplateCUE, `"api-token"`, `"registry"`, 1), "sensitive: true", "sensitive: false", 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "registry.cue"), []byte(registry), 0o600))
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.ConfigTemplateSources = []analysis.ConfigTemplate{{Name: "api-token", Sensitive: true, Where: "the cluster", CUE: sensitiveTemplateCUE}}
		return c, err
	}
	config := func(template, props string) string {
		return "apiVersion: config.oam.dev/v1alpha1\nkind: Config\nmetadata:\n  name: ci\nspec:\n  templateRef:\n    name: " + template + "\n  properties:\n" + props
	}
	has := func(want string) func([]Diagnostic) bool {
		return func(ds []Diagnostic) bool {
			for _, d := range ds {
				if strings.Contains(d.Message, want) {
					return true
				}
			}
			return false
		}
	}

	_, _, diags := openWith(t, cluster, "auto", root, "ci.yaml", config("registry", "    tokn: x\n"), has("takes no parameter tokn"))
	assert.True(t, has("config template registry takes no parameter tokn")(diags), "%v", diags)

	_, _, diags = openWith(t, cluster, "auto", root, "ci.yaml", config("api-token", "    token: x\n"), has("sensitive"))
	assert.True(t, has("api-token is sensitive")(diags), "%v", diags)

	_, _, diags = openWith(t, cluster, "auto", root, "ci.yaml", config("nope", "    token: x\n"), has("on the cluster"))
	assert.True(t, has("no config template named nope in the workspace or on the cluster")(diags), "%v", diags)
}
