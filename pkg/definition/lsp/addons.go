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
	"sort"
	"time"

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	pkgaddon "github.com/oam-dev/kubevela/pkg/addon"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// MethodAddons lists the addons enabled on the cluster and its addon
// registries. It is this server's own request; it takes no params.
const MethodAddons = "vela/addons"

// MethodRegistryAddons lists the addons a registry offers, fetching it.
// It is this server's own request.
const MethodRegistryAddons = "vela/registryAddons"

// EnabledAddon is an addon enabled on the cluster, as its Application says.
type EnabledAddon struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Registry string `json:"registry,omitempty"`
	// Phase is its Application's: running, runningWorkflow, workflowFailed...
	Phase   string `json:"phase,omitempty"`
	Healthy bool   `json:"healthy"`
}

// AddonRegistry is a registry addons are enabled from.
type AddonRegistry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
}

// AddonsResult are the addons enabled on the cluster and its registries.
type AddonsResult struct {
	Context    string          `json:"context,omitempty"`
	Enabled    []EnabledAddon  `json:"enabled"`
	Registries []AddonRegistry `json:"registries"`
}

// RegistryAddonsParams name the registry.
type RegistryAddonsParams struct {
	Registry string `json:"registry"`
}

// RegistryAddon is an addon a registry offers.
type RegistryAddon struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	URL         string   `json:"url,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Versions are those it offers, newest first.
	Versions []string `json:"versions,omitempty"`
}

// RegistryAddonsResult are the addons a registry offers.
type RegistryAddonsResult struct {
	Registry string          `json:"registry"`
	Addons   []RegistryAddon `json:"addons"`
}

// registryTimeout bounds fetching a registry, which is over the network.
const registryTimeout = 60 * time.Second

// readAddons lists the addons enabled on the cluster, by their Applications
// in vela-system, and its registries.
func readAddons(ctx context.Context, cli ctrlclient.Client) (AddonsResult, error) {
	out := AddonsResult{Enabled: []EnabledAddon{}, Registries: []AddonRegistry{}}
	var apps v1beta1.ApplicationList
	if err := cli.List(ctx, &apps, ctrlclient.InNamespace(types.DefaultKubeVelaNS), ctrlclient.HasLabels{oam.LabelAddonName}); err != nil {
		return out, err
	}
	for _, app := range apps.Items {
		l := app.GetLabels()
		healthy := app.Status.Phase == common.ApplicationRunning
		for _, svc := range app.Status.Services {
			healthy = healthy && svc.Healthy
		}
		out.Enabled = append(out.Enabled, EnabledAddon{Name: l[oam.LabelAddonName], Version: l[oam.LabelAddonVersion], Registry: l[oam.LabelAddonRegistry], Phase: string(app.Status.Phase), Healthy: healthy})
	}
	sort.Slice(out.Enabled, func(i, j int) bool { return out.Enabled[i].Name < out.Enabled[j].Name })
	registries, err := pkgaddon.NewRegistryDataStore(cli).ListRegistries(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range registries {
		out.Registries = append(out.Registries, registryOf(r))
	}
	return out, nil
}

// registryOf is a registry as the view lists it: its kind and where it is.
func registryOf(r pkgaddon.Registry) AddonRegistry {
	out := AddonRegistry{Name: r.Name}
	switch {
	case r.Helm != nil:
		out.Type, out.URL = "helm", r.Helm.URL
		if pkgaddon.IsOCIURL(r.Helm.URL) {
			out.Type = "oci"
		}
	case r.Git != nil:
		out.Type, out.URL = "git", r.Git.URL
	case r.Gitee != nil:
		out.Type, out.URL = "gitee", r.Gitee.URL
	case r.Gitlab != nil:
		out.Type, out.URL = "gitlab", r.Gitlab.URL
	case r.OSS != nil:
		out.Type, out.URL = "oss", r.OSS.Endpoint
	default:
		out.Type = "local"
	}
	return out
}

// readRegistryAddons fetches the addons registry name offers, as vela addon
// list does.
func readRegistryAddons(ctx context.Context, cli ctrlclient.Client, name string) (RegistryAddonsResult, error) {
	out := RegistryAddonsResult{Registry: name, Addons: []RegistryAddon{}}
	registries, err := pkgaddon.NewRegistryDataStore(cli).ListRegistries(ctx)
	if err != nil {
		return out, err
	}
	for _, r := range registries {
		if r.Name != name {
			continue
		}
		var list []*pkgaddon.UIData
		if pkgaddon.IsVersionRegistry(r) {
			vr, err := pkgaddon.ToVersionedRegistry(r)
			if err != nil {
				return out, err
			}
			if list, err = vr.ListAddon(); err != nil {
				return out, err
			}
		} else {
			meta, err := r.ListAddonMeta()
			if err != nil {
				return out, err
			}
			if list, err = r.ListUIData(meta, pkgaddon.CLIMetaOptions); err != nil {
				return out, err
			}
		}
		for _, a := range list {
			if a == nil {
				continue
			}
			versions := a.AvailableVersions
			if len(versions) == 0 && a.Version != "" {
				versions = []string{a.Version}
			}
			out.Addons = append(out.Addons, RegistryAddon{Name: a.Name, Description: a.Description, Icon: a.Icon, URL: a.URL, Tags: a.Tags, Versions: versions})
		}
		sort.Slice(out.Addons, func(i, j int) bool { return out.Addons[i].Name < out.Addons[j].Name })
		return out, nil
	}
	return out, errNoRegistry(name)
}

type errNoRegistry string

func (e errNoRegistry) Error() string { return "there is no addon registry " + string(e) }

// addonsRequest answers the addon requests off the message loop: a
// registry is fetched over the network.
func (s *Server) addonsRequest(msg message) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.addons == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	var p RegistryAddonsParams
	if msg.Method == MethodRegistryAddons {
		if rerr := decode(msg.Params, &p); rerr != nil {
			return rerr
		}
	}
	cluster, id := s.cluster, msg.ID
	go func() {
		var result interface{}
		var err error
		if msg.Method == MethodAddons {
			var r AddonsResult
			r, err = cluster.addons()
			r.Context = cluster.context
			result = r
		} else {
			result, err = cluster.registryAddons(p.Registry)
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
