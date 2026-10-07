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

package analysis

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"sort"
	"sync"

	"cuelang.org/go/cue/parser"
)

// Where an Application's definition comes from, most preferred first.
const (
	SourceWorkspace = "workspace"
	SourceCluster   = "cluster"
	SourceBuiltin   = "built-in"
)

// AppDefinition is a definition an Application may name.
type AppDefinition struct {
	Name        string
	Type        string
	Description string
	// Source is where it was found: the workspace, the cluster or built in.
	Source string
	// Path is its file, for one in the workspace.
	Path string
	// CUE is the definition, header and template.
	CUE string
}

// AppDefinitions are the definitions an Application may name, by type and
// name, each from the most preferred source that has it.
type AppDefinitions interface {
	Lookup(defType, name string) (AppDefinition, bool)
	List(defType string) []AppDefinition
}

// builtinDefinitionsFile bundles KubeVela's own definitions.
const builtinDefinitionsFile = "builtin_definitions.json.gz"

//go:embed builtin_definitions.json.gz
var builtinDefinitionsBundle []byte

// BuiltinDefinitions are KubeVela's own definitions, as this vela ships them.
var BuiltinDefinitions = sync.OnceValue(func() []AppDefinition {
	zr, err := gzip.NewReader(bytes.NewReader(builtinDefinitionsBundle))
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	var files [][2]string
	if json.Unmarshal(data, &files) != nil {
		return nil
	}
	var out []AppDefinition
	for _, f := range files {
		if d, ok := AppDefinitionOf(f[0], f[1], SourceBuiltin); ok {
			out = append(out, d)
		}
	}
	return out
})

// AppDefinitionOf reads the definition in src, a CUE definition file.
func AppDefinitionOf(path, src, source string) (AppDefinition, bool) {
	f, err := parser.ParseFile(path, src)
	if err != nil {
		return AppDefinition{}, false
	}
	d, ok := newDocument(path, []byte(src), f)
	if !ok || d.typ == "" {
		return AppDefinition{}, false
	}
	return AppDefinition{Name: d.name, Type: d.typ, Description: d.headerString("description"), Source: source, CUE: src}, true
}

// LayeredDefinitions finds a definition in the first layer that has it.
type LayeredDefinitions [][]AppDefinition

// Lookup is the definition of a type and name, from the first layer with it.
func (l LayeredDefinitions) Lookup(defType, name string) (AppDefinition, bool) {
	for _, layer := range l {
		for _, d := range layer {
			if d.Type == defType && d.Name == name {
				return d, true
			}
		}
	}
	return AppDefinition{}, false
}

// List is every definition of a type, each from the first layer with it,
// by name.
func (l LayeredDefinitions) List(defType string) []AppDefinition {
	seen := map[string]bool{}
	var out []AppDefinition
	for _, layer := range l {
		for _, d := range layer {
			if d.Type == defType && !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
