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

package common

import (
	"encoding/json"
	"testing"

	"cuelang.org/go/cue/cuecontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// properties are the generated schema's parameter properties.
func properties(t *testing.T, src string) map[string]interface{} {
	t.Helper()
	out, err := GenOpenAPI(cuecontext.New().CompileString(src))
	require.NoError(t, err)
	var doc struct {
		Components struct {
			Schemas map[string]map[string]interface{} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(out, &doc))
	return doc.Components.Schemas["parameter"]["properties"].(map[string]interface{})
}

// A bounded value with a default keeps both: CUE's encoder rejects the default
// beside the bounds, so the default is set in the schema made without it.
func TestGenOpenAPIBoundedDefaults(t *testing.T) {
	props := properties(t, `
import "strings"
parameter: {
	// +usage=Replicas of the cache
	replicas: *1 | int & >=1 & <=9
	ratio:    *0.5 | number & >0 & <1
	size:     *"1Gi" | string & =~"^[0-9]+Gi$"
	name?:    strings.MinRunes(3) & strings.MaxRunes(40)
	plain:    *7 | int
	nested: {
		port: *8080 | int & >=1 & <=65535
	}
	items: [...{
		weight: *1 | int & >=0
	}]
}`)
	assert.Equal(t, map[string]interface{}{"type": "integer", "default": float64(1), "minimum": float64(1), "maximum": float64(9), "description": "+usage=Replicas of the cache"}, props["replicas"])
	assert.Equal(t, float64(0.5), props["ratio"].(map[string]interface{})["default"])
	assert.Equal(t, true, props["ratio"].(map[string]interface{})["exclusiveMinimum"], "OpenAPI 3.0's form")
	assert.Equal(t, float64(0), props["ratio"].(map[string]interface{})["minimum"])
	assert.Equal(t, "1Gi", props["size"].(map[string]interface{})["default"])
	assert.Equal(t, "^[0-9]+Gi$", props["size"].(map[string]interface{})["pattern"])
	assert.Equal(t, float64(3), props["name"].(map[string]interface{})["minLength"])
	assert.Equal(t, float64(7), props["plain"].(map[string]interface{})["default"])
	port := props["nested"].(map[string]interface{})["properties"].(map[string]interface{})["port"].(map[string]interface{})
	assert.Equal(t, float64(8080), port["default"])
	assert.Equal(t, float64(65535), port["maximum"])
	weight := props["items"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})["weight"].(map[string]interface{})
	assert.Equal(t, float64(1), weight["default"])
	assert.Equal(t, float64(0), weight["minimum"])
}
