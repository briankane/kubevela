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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"cuelang.org/go/cue"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/schema"
	webhookapp "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/application"
)

// MethodDefinitionSchema gives a definition's parameter as the JSON schema
// the controller publishes for VelaUX's forms, with the UI schema that
// refines the form, where there is one. It is this server's own request.
const MethodDefinitionSchema = "vela/definitionSchema"

// DefinitionSchemaParams name the definition by its type and name.
type DefinitionSchemaParams struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// DefinitionSchemaResult is a definition's description, where it comes from,
// its parameter's schema, and its UI schema, null where it has none.
type DefinitionSchemaResult struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Source      string          `json:"source"`
	Schema      json.RawMessage `json:"schema"`
	UISchema    json.RawMessage `json:"uiSchema"`
}

// uiSchemaTypes are the definition types as a UI schema's name spells them.
var uiSchemaTypes = map[string]string{"workflow-step": "workflowstep"}

// uiSchemaName is the name of a definition's UI schema, as VelaUX looks it up.
func uiSchemaName(typ, name string) string {
	if t, ok := uiSchemaTypes[typ]; ok {
		typ = t
	}
	return typ + "-uischema-" + name
}

// parameterSchema is a definition's parameter as the controller publishes it
// for VelaUX. A template that does not compile on its own, as one extending
// another or importing a workspace package, has its parameter compiled alone.
func parameterSchema(def analysis.AppDefinition) ([]byte, error) {
	tmpl, ok := analysis.TemplateSource(def.Name+".cue", []byte(def.CUE))
	if !ok {
		return nil, fmt.Errorf("%s has no template", def.Name)
	}
	s, err := schema.ParsePropertiesToSchema(context.Background(), tmpl.Body)
	if err != nil {
		param, found := webhookapp.TargetParameter(tmpl.Body)
		if !found {
			return nil, err
		}
		s, err = schema.ParseValueToSchema(param.Context().CompileString("{}").FillPath(cue.ParsePath("parameter"), param))
		if err != nil {
			return nil, err
		}
	}
	return s.MarshalJSON()
}

// workspaceUISchema is the UI schema file a workspace folder holds for a
// definition, as an addon's schemas/ holds it, as JSON.
func workspaceUISchema(folders []string, name string) (json.RawMessage, bool) {
	var found string
	for _, folder := range folders {
		_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return filepath.SkipDir
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if !d.IsDir() && (d.Name() == name+".yaml" || d.Name() == name+".yml") {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			break
		}
	}
	if found == "" {
		return nil, false
	}
	data, err := os.ReadFile(found) //nolint:gosec // a file of the workspace's own
	if err != nil {
		return nil, false
	}
	out, err := yaml.YAMLToJSON(data)
	return out, err == nil
}

// readUISchema reads a definition's UI schema from the cluster's ConfigMap,
// where VelaUX reads it.
func readUISchema(cfg *rest.Config, name string) (string, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterTimeout)
	defer cancel()
	cm, err := cs.CoreV1().ConfigMaps(types.DefaultKubeVelaNS).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return cm.Data[types.UISchema], nil
}

// definitionSchema answers MethodDefinitionSchema off the message loop, as
// it may read the cluster.
func (s *Server) definitionSchema(id json.RawMessage, p DefinitionSchemaParams) {
	def, ok := s.options().Applications.Lookup(p.Type, p.Name)
	if !ok {
		_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: fmt.Sprintf("no %s definition named %s", p.Type, p.Name)})
		return
	}
	folders := append([]string(nil), s.folders...)
	var readCluster func(string) (string, error)
	if s.clusterEnabled && s.cluster != nil && s.cluster.err == nil {
		readCluster = s.cluster.uiSchema
	}
	go func() {
		result := DefinitionSchemaResult{Name: def.Name, Type: p.Type, Description: def.Description, Source: def.Source, UISchema: json.RawMessage("null")}
		out, err := parameterSchema(def)
		if err != nil {
			s.post(func() {
				_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: fmt.Sprintf("the parameter of %s gives no schema: %v", def.Name, err)})
			})
			return
		}
		result.Schema = out
		name := uiSchemaName(p.Type, def.Name)
		if ui, ok := workspaceUISchema(folders, name); ok {
			result.UISchema = ui
		} else if readCluster != nil {
			if ui, err := readCluster(name); err == nil && ui != "" && json.Valid([]byte(ui)) {
				result.UISchema = json.RawMessage(ui)
			}
		}
		s.post(func() { _ = s.reply(id, result, nil) })
	}()
}
