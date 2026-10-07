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
	"encoding/json"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A parameter CUE's OpenAPI generator refuses is read field by field: its
// type, default, whether it must be given, its help and its choices.
func TestFallbackSchema(t *testing.T) {
	v := cuecontext.New().CompileString(`parameter: {
	// +usage=How many to run
	replicas: *1 | int & >=1
	// +usage=Where it runs
	namespace: string
	mode?: "fast" | "safe"
	labels?: [string]: string
	ports?: [...{port: int, name?: string}]
	tls: {enabled: *false | bool}
}`).LookupPath(cue.ParsePath("parameter"))
	require.NoError(t, v.Err())
	b, err := json.Marshal(fallbackSchema(v))
	require.NoError(t, err)
	var s struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type                 string                     `json:"type"`
			Default              interface{}                `json:"default"`
			Description          string                     `json:"description"`
			Enum                 []interface{}              `json:"enum"`
			Items                map[string]interface{}     `json:"items"`
			AdditionalProperties map[string]interface{}     `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(b, &s))
	assert.Equal(t, "object", s.Type)
	assert.ElementsMatch(t, []string{"namespace", "tls"}, s.Required, "what has no default and is not optional")
	assert.Equal(t, "integer", s.Properties["replicas"].Type)
	assert.EqualValues(t, 1, s.Properties["replicas"].Default)
	assert.Equal(t, "How many to run", s.Properties["replicas"].Description)
	assert.Equal(t, []interface{}{"fast", "safe"}, s.Properties["mode"].Enum)
	assert.Equal(t, "string", s.Properties["labels"].AdditionalProperties["type"])
	assert.Equal(t, "object", s.Properties["ports"].Items["type"])
	assert.Contains(t, s.Properties["tls"].Properties, "enabled")
}
