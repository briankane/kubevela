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
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	pkgtypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configv1alpha1 "github.com/oam-dev/kubevela/apis/config.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/config"
	"github.com/oam-dev/kubevela/pkg/utils/common"
)

// MethodConfigs lists the cluster's config templates and configs. It is this
// server's own request.
const MethodConfigs = "vela/configs"

// MethodConfigTemplate reads what a config template's form is made from.
const MethodConfigTemplate = "vela/configTemplate"

// MethodConfigProperties reads a config's values, unless its template is
// sensitive.
const MethodConfigProperties = "vela/configProperties"

// Where a config or config template is stored: a ConfigTemplate or Config
// resource, or the ConfigMap or Secret KubeVela wrote before those existed.
const (
	storedResource  = "resource"
	storedConfigMap = "configmap"
	storedSecret    = "secret"
)

// ConfigTemplateInfo is what a list shows of a config template.
type ConfigTemplateInfo struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Alias       string `json:"alias,omitempty"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Stored      string `json:"stored"`
}

// ConfigInfo is what a list shows of a config.
type ConfigInfo struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Template          string `json:"template"`
	TemplateNamespace string `json:"templateNamespace"`
	Alias             string `json:"alias,omitempty"`
	Description       string `json:"description,omitempty"`
	// Phase is a Config resource's status phase; a Secret has none.
	Phase   string `json:"phase,omitempty"`
	Created string `json:"created"`
	Stored  string `json:"stored"`
}

// ConfigsResult is every config template and config on the cluster.
type ConfigsResult struct {
	Context   string               `json:"context"`
	Templates []ConfigTemplateInfo `json:"templates"`
	Configs   []ConfigInfo         `json:"configs"`
}

// ConfigParams name a config or config template.
type ConfigParams struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ConfigTemplateResult is what a config template's form is made from: the
// JSON schema of its parameter, and its UI schema, null when it has none.
type ConfigTemplateResult struct {
	Context   string          `json:"context"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Sensitive bool            `json:"sensitive"`
	Schema    json.RawMessage `json:"schema"`
	UISchema  json.RawMessage `json:"uiSchema"`
}

// ConfigPropertiesResult is a config's values, or, in Hidden, why they are
// not read back.
type ConfigPropertiesResult struct {
	Context    string                 `json:"context"`
	Properties map[string]interface{} `json:"properties"`
	Hidden     string                 `json:"hidden,omitempty"`
}

// noConfigCRD reports whether err means the cluster serves no Config or
// ConfigTemplate resources.
func noConfigCRD(err error) bool {
	return meta.IsNoMatchError(err) || apierrors.IsNotFound(err)
}

// templateNamespace is the namespace a Config's template reference names;
// unset, it is vela-system's.
func templateNamespace(ref *configv1alpha1.ConfigTemplateReference) string {
	if ref.Namespace == "" {
		return types.DefaultKubeVelaNS
	}
	return ref.Namespace
}

// readConfigs lists every config template and config, in every namespace,
// from both storages.
func readConfigs(ctx context.Context, cli ctrlclient.Client) (ConfigsResult, error) {
	out := ConfigsResult{Templates: []ConfigTemplateInfo{}, Configs: []ConfigInfo{}}
	f := config.NewConfigFactory(cli)
	templates, err := f.ListTemplates(ctx, "", "")
	if err != nil {
		return out, err
	}
	// The factory reads both storages into one model, so which it read is
	// told by whether a ConfigTemplate resource has the name.
	var crs configv1alpha1.ConfigTemplateList
	if err := cli.List(ctx, &crs); err != nil && !noConfigCRD(err) {
		return out, err
	}
	resource := map[string]bool{}
	for _, ct := range crs.Items {
		resource[ct.Namespace+"/"+ct.Name] = true
	}
	for _, t := range templates {
		stored := storedConfigMap
		if resource[t.Namespace+"/"+t.Name] {
			stored = storedResource
		}
		out.Templates = append(out.Templates, ConfigTemplateInfo{Name: t.Name, Namespace: t.Namespace, Alias: t.Alias, Description: t.Description, Scope: t.Scope, Sensitive: t.Sensitive, Stored: stored})
	}

	var resources configv1alpha1.ConfigList
	if err := cli.List(ctx, &resources); err != nil && !noConfigCRD(err) {
		return out, err
	}
	for _, c := range resources.Items {
		info := ConfigInfo{Name: c.Name, Namespace: c.Namespace, Alias: c.Spec.Alias, Description: c.Spec.Description, Phase: string(c.Status.Phase), Created: c.CreationTimestamp.UTC().Format(time.RFC3339), Stored: storedResource}
		if c.Spec.TemplateRef != nil {
			info.Template, info.TemplateNamespace = c.Spec.TemplateRef.Name, templateNamespace(c.Spec.TemplateRef)
		}
		out.Configs = append(out.Configs, info)
	}
	secrets, err := f.ListConfigs(ctx, "", "", "", false)
	if err != nil {
		return out, err
	}
	for _, c := range secrets {
		out.Configs = append(out.Configs, ConfigInfo{Name: c.Name, Namespace: c.Namespace, Template: c.Template.Name, TemplateNamespace: c.Template.Namespace, Alias: c.Alias, Description: c.Description, Created: c.CreateTime.UTC().Format(time.RFC3339), Stored: storedSecret})
	}
	sort.Slice(out.Templates, func(i, j int) bool { return out.Templates[i].Name < out.Templates[j].Name })
	sort.Slice(out.Configs, func(i, j int) bool {
		a, b := out.Configs[i], out.Configs[j]
		if a.Template != b.Template {
			return a.Template < b.Template
		}
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})
	return out, nil
}

// readConfigTemplate reads a config template's parameter schema, and its UI
// schema from the config-uischema-<name> ConfigMap in vela-system.
func readConfigTemplate(ctx context.Context, cli ctrlclient.Client, namespace, name string) (ConfigTemplateResult, error) {
	t, err := config.NewConfigFactory(cli).LoadTemplate(ctx, name, namespace)
	if err != nil {
		return ConfigTemplateResult{}, fmt.Errorf("config template %s/%s: %w", namespace, name, err)
	}
	out := ConfigTemplateResult{Name: t.Name, Namespace: t.Namespace, Sensitive: t.Sensitive, Schema: json.RawMessage("null"), UISchema: json.RawMessage("null")}
	if t.Schema != nil {
		if out.Schema, err = t.Schema.MarshalJSON(); err != nil {
			return out, err
		}
	}
	var ui corev1.ConfigMap
	if err := cli.Get(ctx, pkgtypes.NamespacedName{Namespace: types.DefaultKubeVelaNS, Name: "config-uischema-" + name}, &ui); err == nil {
		if j, err := yaml.YAMLToJSON([]byte(ui.Data[types.UISchema])); err == nil && string(j) != "null" {
			out.UISchema = j
		}
	}
	return out, nil
}

// readConfigProperties reads a config's values, from its Config resource or
// its Secret. A sensitive template's are never read back.
func readConfigProperties(ctx context.Context, cli ctrlclient.Client, namespace, name string) (ConfigPropertiesResult, error) {
	f := config.NewConfigFactory(cli)
	var c configv1alpha1.Config
	switch err := cli.Get(ctx, pkgtypes.NamespacedName{Namespace: namespace, Name: name}, &c); {
	case err == nil:
		if c.Spec.TemplateRef != nil {
			if t, err := f.LoadTemplate(ctx, c.Spec.TemplateRef.Name, templateNamespace(c.Spec.TemplateRef)); err == nil && t.Sensitive {
				return ConfigPropertiesResult{Hidden: fmt.Sprintf("its template %s is sensitive, so its values are never read back", t.Name)}, nil
			}
		}
		if c.Spec.PropertiesFrom != nil {
			return ConfigPropertiesResult{Hidden: fmt.Sprintf("its values are in Secret %s", c.Spec.PropertiesFrom.SecretRef.Name)}, nil
		}
		out := ConfigPropertiesResult{Properties: map[string]interface{}{}}
		if c.Spec.Properties != nil && len(c.Spec.Properties.Raw) > 0 {
			if err := json.Unmarshal(c.Spec.Properties.Raw, &out.Properties); err != nil {
				return out, err
			}
		}
		return out, nil
	case !noConfigCRD(err):
		return ConfigPropertiesResult{}, err
	}
	props, err := f.ReadConfig(ctx, namespace, name)
	if errors.Is(err, config.ErrSensitiveConfig) {
		return ConfigPropertiesResult{Hidden: "its template is sensitive, so its values are never read back"}, nil
	}
	if err != nil {
		return ConfigPropertiesResult{}, fmt.Errorf("config %s/%s: %w", namespace, name, err)
	}
	return ConfigPropertiesResult{Properties: props}, nil
}

// configClient is a client of the cluster that knows KubeVela's kinds.
func configClient(cfg *rest.Config) (ctrlclient.Client, error) {
	return ctrlclient.New(cfg, ctrlclient.Options{Scheme: common.Scheme})
}

// withConfigClient calls do with a client of cfg, bounded in time.
func withConfigClient[T any](cfg *rest.Config, do func(context.Context, ctrlclient.Client) (T, error)) (T, error) {
	var zero T
	cli, err := configClient(cfg)
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*clusterTimeout)
	defer cancel()
	return do(ctx, cli)
}

// configRequest answers the config requests off the message loop: listing
// templates compiles each one's CUE.
func (s *Server) configRequest(msg message) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.configs == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	var p ConfigParams
	if msg.Method != MethodConfigs {
		if rerr := decode(msg.Params, &p); rerr != nil {
			return rerr
		}
	}
	cluster, id := s.cluster, msg.ID
	go func() {
		var result interface{}
		var err error
		switch msg.Method {
		case MethodConfigs:
			var r ConfigsResult
			r, err = cluster.configs()
			r.Context = cluster.context
			result = r
		case MethodConfigTemplate:
			var r ConfigTemplateResult
			r, err = cluster.configTemplate(p.Namespace, p.Name)
			r.Context = cluster.context
			result = r
		default:
			var r ConfigPropertiesResult
			r, err = cluster.configProperties(p.Namespace, p.Name)
			r.Context = cluster.context
			result = r
		}
		s.post(func() {
			if err != nil {
				_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: err.Error()})
				return
			}
			_ = s.reply(id, result, nil)
		})
	}()
	return nil
}
