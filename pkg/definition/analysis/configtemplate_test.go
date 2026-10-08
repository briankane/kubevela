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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A config template outside an addon is checked as vela config-template
// apply reads it; anything else is left to the other checks.
func TestCheckConfigTemplateFile(t *testing.T) {
	builtin, err := os.ReadFile(filepath.Join("..", "..", "..", "references", "cli", "test-data", "config-templates", "image-registry.cue"))
	require.NoError(t, err)
	cases := map[string]struct {
		src  string
		want []string
	}{
		"a valid config template":   {src: goodConfigTemplate},
		"KubeVela's image-registry": {src: string(builtin)},
		"a scope that is not one": {
			src:  strings.Replace(goodConfigTemplate, `"system"`, `"cluster"`, 1),
			want: []string{"7: ", "scope"},
		},
		"a name that cannot name its ConfigMap": {
			src:  strings.Replace(goodConfigTemplate, `"image-registry"`, `"Image_Registry"`, 1),
			want: []string{"4: metadata.name names the template's ConfigMap or ConfigTemplate: lower case letters, digits and hyphens"},
		},
		"no parameter": {
			src:  "metadata: name: \"x\"\ntemplate: output: type: \"Opaque\"\n",
			want: []string{"template.parameter must be set"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diags, ok := CheckConfigTemplateFile(filepath.Join(t.TempDir(), "registry.cue"), []byte(tc.src))
			require.True(t, ok, "it is a config template")
			var got []string
			for _, d := range withoutInfo(diags) {
				got = append(got, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
			}
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}

	for name, src := range map[string]string{
		"a definition":          "web: {type: \"component\"}\ntemplate: output: {}\n",
		"not CUE":               "metadata: {\n",
		"plain CUE":             "a: 1\n",
		"metadata, no template": "metadata: name: \"x\"\n",
	} {
		_, ok := CheckConfigTemplateFile(filepath.Join(t.TempDir(), "x.cue"), []byte(src))
		assert.False(t, ok, name)
	}
}
