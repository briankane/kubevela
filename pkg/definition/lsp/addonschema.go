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
	"os"
	"path/filepath"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/schema"
)

// MethodAddonSchema reads an addon folder for its panel: its metadata, its
// parameters' JSON schema, as VelaUX's addon form uses it, and its UI schema.
// It is this server's own request.
const MethodAddonSchema = "vela/addonSchema"

// AddonSchemaParams name the addon's folder.
type AddonSchemaParams struct {
	Folder string `json:"folder"`
}

// AddonSchemaResult is an addon's metadata, its parameters' schema, null
// where it takes none, and its UI schema, null where it has none.
type AddonSchemaResult struct {
	Name         string          `json:"name"`
	Version      string          `json:"version,omitempty"`
	Description  string          `json:"description,omitempty"`
	Dependencies []string        `json:"dependencies"`
	Schema       json.RawMessage `json:"schema"`
	UISchema     json.RawMessage `json:"uiSchema"`
	// SchemaError is why its parameters give no schema, where they do not.
	SchemaError string `json:"schemaError,omitempty"`
	// EnableError is why vela addon enable would refuse it: it makes the
	// parameters' schema first, as Schema is made where it can be.
	EnableError string `json:"enableError,omitempty"`
}

// addonMetadata is the part of an addon's metadata.yaml its panel shows.
type addonMetadata struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	Dependencies []struct {
		Name string `json:"name"`
	} `json:"dependencies"`
}

// addonSchema reads an addon folder. The parameters' schema is generated as
// for a definition's, from the parameter.cue that declares them.
func addonSchema(folder string) (AddonSchemaResult, error) {
	raw, err := os.ReadFile(filepath.Join(folder, "metadata.yaml")) //nolint:gosec // the workspace's own addon
	if err != nil {
		return AddonSchemaResult{}, fmt.Errorf("%s is not an addon: %w", folder, err)
	}
	var meta addonMetadata
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return AddonSchemaResult{}, fmt.Errorf("metadata.yaml: %w", err)
	}
	out := AddonSchemaResult{Name: meta.Name, Version: meta.Version, Description: meta.Description, Dependencies: []string{}, Schema: json.RawMessage("null"), UISchema: json.RawMessage("null")}
	for _, d := range meta.Dependencies {
		out.Dependencies = append(out.Dependencies, d.Name)
	}
	if param, err := os.ReadFile(filepath.Join(folder, "parameter.cue")); err == nil { //nolint:gosec // the workspace's own addon
		if s, err := schema.ParsePropertiesToSchema(context.Background(), string(param)); err == nil {
			if b, err := s.MarshalJSON(); err == nil {
				out.Schema = b
			}
		} else if v := cuecontext.New().CompileBytes(param).LookupPath(cue.ParsePath("parameter")); v.Err() == nil && v.Exists() {
			// The generator refuses some constraints it could not express: read it field by field.
			out.EnableError = fmt.Sprintf("vela addon enable refuses this addon: it makes a schema of its parameters, and CUE's generator cannot (%v)", err)
			if b, err := json.Marshal(fallbackSchema(v)); err == nil {
				out.Schema = b
			}
		} else {
			out.SchemaError = err.Error()
		}
	}
	if ui, ok := workspaceUISchema([]string{filepath.Join(folder, "schemas")}, "addon-uischema-"+meta.Name); ok {
		out.UISchema = ui
	}
	return out, nil
}

// panelRequest answers the requests the extension's panels make of files:
// an addon's schema, and where a field is.
func (s *Server) panelRequest(msg message) (interface{}, *ResponseError) {
	switch msg.Method {
	case MethodAddonSchema:
		var p AddonSchemaParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		r, err := addonSchema(p.Folder)
		if err != nil {
			return nil, &ResponseError{Code: CodeInvalidParams, Message: err.Error()}
		}
		return r, nil
	default:
		var p LocateParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		if r, ok := s.locate(p); ok {
			return r, nil
		}
		return nil, nil
	}
}
