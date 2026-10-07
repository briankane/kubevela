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
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/definition"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// clusterDefinitions are the cluster's definitions, each turned into CUE
// when first asked for.
type clusterDefinitions struct {
	objects map[string]unstructured.Unstructured
	cue     map[string]analysis.AppDefinition
}

func newClusterDefinitions(objs []unstructured.Unstructured) *clusterDefinitions {
	c := &clusterDefinitions{objects: map[string]unstructured.Unstructured{}, cue: map[string]analysis.AppDefinition{}}
	for _, o := range objs {
		typ := definition.DefinitionKindToType[o.GetKind()]
		key := typ + "/" + o.GetName()
		// vela-system's is the one vela def apply writes, so it wins.
		if _, seen := c.objects[key]; !seen || o.GetNamespace() == definitionNamespace {
			c.objects[key] = o
		}
	}
	return c
}

func (c *clusterDefinitions) lookup(defType, name string) (analysis.AppDefinition, bool) {
	key := defType + "/" + name
	if d, ok := c.cue[key]; ok {
		return d, true
	}
	obj, ok := c.objects[key]
	if !ok {
		return analysis.AppDefinition{}, false
	}
	def := definition.Definition{Unstructured: obj}
	src, err := def.ToCUEString()
	if err != nil {
		return analysis.AppDefinition{}, false
	}
	d, ok := analysis.AppDefinitionOf(obj.GetName()+".cue", src, analysis.SourceCluster)
	if !ok {
		return analysis.AppDefinition{}, false
	}
	c.cue[key] = d
	return d, true
}

func (c *clusterDefinitions) names(defType string) []string {
	var out []string
	for _, o := range c.objects {
		if definition.DefinitionKindToType[o.GetKind()] == defType {
			out = append(out, o.GetName())
		}
	}
	return out
}

// appDefinitions are the definitions an Application may name: the
// workspace's, then the cluster's, then those built in.
type appDefinitions struct {
	s *Server
}

func (a appDefinitions) workspace(defType, name string) (analysis.AppDefinition, bool) {
	for path, d := range a.s.definitions {
		if d.defType == defType && d.name == name {
			def, ok := analysis.AppDefinitionOf(path, a.s.textOf(path), analysis.SourceWorkspace)
			def.Path = path
			return def, ok
		}
	}
	return analysis.AppDefinition{}, false
}

// Lookup is the definition of a type and name, from the first source with it.
func (a appDefinitions) Lookup(defType, name string) (analysis.AppDefinition, bool) {
	if d, ok := a.workspace(defType, name); ok {
		return d, true
	}
	if a.s.clusterDefs != nil {
		if d, ok := a.s.clusterDefs.lookup(defType, name); ok {
			return d, true
		}
	}
	return analysis.LayeredDefinitions{analysis.BuiltinDefinitions()}.Lookup(defType, name)
}

// List is every definition of a type, each from the first source with it.
func (a appDefinitions) List(defType string) []analysis.AppDefinition {
	seen := map[string]bool{}
	var names []string
	for _, d := range a.s.definitions {
		if d.defType == defType && !seen[d.name] {
			seen[d.name] = true
			names = append(names, d.name)
		}
	}
	if a.s.clusterDefs != nil {
		for _, n := range a.s.clusterDefs.names(defType) {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	for _, d := range analysis.BuiltinDefinitions() {
		if d.Type == defType && !seen[d.Name] {
			seen[d.Name] = true
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	out := make([]analysis.AppDefinition, 0, len(names))
	for _, n := range names {
		if d, ok := a.Lookup(defType, n); ok {
			out = append(out, d)
		}
	}
	return out
}
