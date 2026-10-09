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
	"os"
	"reflect"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/definition"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// MethodDefinitionsOverview lists the definitions in the workspace and on
// the cluster, each with where it is and whether the two differ.
const MethodDefinitionsOverview = "vela/definitionsOverview"

// DefinitionsOverviewParams ask for the cluster's definitions to be read
// again first, as after one is applied.
type DefinitionsOverviewParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// DefinitionOverview is a definition by type and name: its workspace file,
// its namespace, latest revision and installing addon on the cluster, and
// State: workspace or cluster when only there, same or changed when both.
type DefinitionOverview struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Addon     string `json:"addon,omitempty"`
	State     string `json:"state"`
}

// DefinitionsOverviewResult is the overview, and the context the cluster's
// were read from; none when the cluster is not read.
type DefinitionsOverviewResult struct {
	Context     string               `json:"context,omitempty"`
	Definitions []DefinitionOverview `json:"definitions"`
}

// workspaceDefinition is a workspace file's definition and its source.
type workspaceDefinition struct {
	Name, Type, Path string
	Src              []byte
}

// sameDefinition is whether src, applied with vela def apply, would write
// what the cluster holds: the spec, and the description it carries.
func sameDefinition(src []byte, cluster unstructured.Unstructured) (bool, error) {
	def := definition.Definition{Unstructured: unstructured.Unstructured{}}
	if err := def.FromCUEString(string(src), nil); err != nil {
		return false, err
	}
	desc := func(u unstructured.Unstructured) string { return u.GetAnnotations()[definition.DescriptionKey] }
	return reflect.DeepEqual(def.Object["spec"], cluster.Object["spec"]) && desc(def.Unstructured) == desc(cluster), nil
}

// definitionsOverview merges the workspace's definitions with the cluster's,
// by type and name. A cluster definition in vela-system is the one vela def
// apply writes, so it is the one a workspace file is compared with.
func definitionsOverview(workspace []workspaceDefinition, cluster []unstructured.Unstructured) []DefinitionOverview {
	byKey := map[string]*DefinitionOverview{}
	objects := map[string]unstructured.Unstructured{}
	for _, o := range cluster {
		typ := definition.DefinitionKindToType[o.GetKind()]
		key := typ + "/" + o.GetName()
		if have, ok := objects[key]; ok && have.GetNamespace() == oam.SystemDefinitionNamespace {
			continue
		}
		objects[key] = o
		rev, _, _ := unstructured.NestedInt64(o.Object, "status", "latestRevision", "revision")
		byKey[key] = &DefinitionOverview{Name: o.GetName(), Type: typ, Namespace: o.GetNamespace(), Revision: rev, Addon: o.GetLabels()[oam.LabelAddonName], State: "cluster"}
	}
	for _, w := range workspace {
		key := w.Type + "/" + w.Name
		d, ok := byKey[key]
		if !ok {
			byKey[key] = &DefinitionOverview{Name: w.Name, Type: w.Type, Path: w.Path, State: "workspace"}
			continue
		}
		d.Path = w.Path
		d.State = "changed"
		if same, err := sameDefinition(w.Src, objects[key]); err == nil && same {
			d.State = "same"
		}
	}
	out := make([]DefinitionOverview, 0, len(byKey))
	for _, d := range byKey {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// definitionsOverviewRequest answers MethodDefinitionsOverview off the
// message loop: comparing a file with the cluster's compiles its CUE.
func (s *Server) definitionsOverviewRequest(msg message) *ResponseError {
	var p DefinitionsOverviewParams
	if rerr := decode(msg.Params, &p); rerr != nil {
		return rerr
	}
	open := map[string]string{}
	for uri, text := range s.docs {
		open[pathOf(uri)] = text
	}
	entries := make([]workspaceDefinition, 0, len(s.definitions))
	for path, d := range s.definitions {
		entries = append(entries, workspaceDefinition{Name: d.name, Type: d.defType, Path: path})
	}
	cluster := s.cluster
	read := s.clusterEnabled && cluster != nil && cluster.err == nil && cluster.vela
	var objects []unstructured.Unstructured
	if read && s.clusterDefs != nil {
		for _, o := range s.clusterDefs.objects {
			objects = append(objects, o)
		}
	}
	var relist func() []unstructured.Unstructured
	if read && p.Refresh {
		relist = cluster.listDefinitions
	}
	id := msg.ID
	go func() {
		for i := range entries {
			if text, ok := open[entries[i].Path]; ok {
				entries[i].Src = []byte(text)
			} else if src, err := os.ReadFile(entries[i].Path); err == nil {
				entries[i].Src = src
			}
		}
		if relist != nil {
			objects = relist()
		}
		result := DefinitionsOverviewResult{Definitions: definitionsOverview(entries, objects)}
		if read {
			result.Context = cluster.context
		}
		s.post(func() {
			if relist != nil && s.cluster == cluster {
				s.clusterDefs = newClusterDefinitions(objects)
				s.republish()
			}
			_ = s.reply(id, result, nil)
		})
	}()
	return nil
}
