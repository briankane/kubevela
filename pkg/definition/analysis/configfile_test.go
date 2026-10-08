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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodConfig = `apiVersion: config.oam.dev/v1alpha1
kind: Config
metadata:
  name: ghcr
  namespace: vela-system
spec:
  templateRef:
    name: registry
  alias: GitHub
  properties:
    registry: ghcr.io
`

// configTemplates are a plain registry template and a sensitive token one.
func configTemplates(t *testing.T) map[string]ConfigTemplate {
	t.Helper()
	registry := strings.Replace(goodConfigTemplate, `"image-registry"`, `"registry"`, 1)
	registry = strings.Replace(registry, "sensitive:   true", "sensitive:   false", 1)
	return map[string]ConfigTemplate{
		"registry": {Name: "registry", Where: "the workspace", CUE: registry},
		"token":    {Name: "token", Where: "the cluster", Sensitive: true, CUE: strings.Replace(goodConfigTemplate, `"image-registry"`, `"token"`, 1)},
	}
}

func TestCheckConfigFile(t *testing.T) {
	cases := map[string]struct {
		src         string
		clusterRead bool
		want        []string
	}{
		"a valid config": {src: goodConfig},
		"a property the template does not take": {
			src:  strings.Replace(goodConfig, "    registry: ghcr.io", "    registry: ghcr.io\n    registy: x", 1),
			want: []string{"12: config template registry takes no parameter registy"},
		},
		"a required property missing": {
			src:  strings.Replace(goodConfig, "    registry: ghcr.io", "    token: x", 1),
			want: []string{"8: config template registry requires registry in properties"},
		},
		"a property of the wrong type": {
			src:  strings.Replace(goodConfig, "registry: ghcr.io", "registry: 5", 1),
			want: []string{"11: ", "registry"},
		},
		"a template the workspace does not have": {
			src:  strings.Replace(goodConfig, "name: registry", "name: registri", 1),
			want: []string{"8: no config template named registri in the workspace"},
		},
		"a template neither has, the cluster read": {
			src:         strings.Replace(goodConfig, "name: registry", "name: registri", 1),
			clusterRead: true,
			want:        []string{"8: no config template named registri in the workspace or on the cluster"},
		},
		"properties and propertiesFrom both": {
			src:  goodConfig + "  propertiesFrom:\n    secretRef:\n      name: ghcr-properties\n",
			want: []string{"12: a Config takes its values from properties or propertiesFrom, not both"},
		},
		"values of a sensitive template written in the file": {
			src:  strings.Replace(goodConfig, "name: registry", "name: token", 1),
			want: []string{"10: token is sensitive: this file holds its values in plain text; keep them in a Secret named by propertiesFrom.secretRef"},
		},
		"a sensitive template's values in a Secret": {
			src: strings.Replace(strings.Replace(goodConfig, "name: registry", "name: token", 1), "  properties:\n    registry: ghcr.io\n", "  propertiesFrom:\n    secretRef:\n      name: ghcr-properties\n", 1),
		},
		"a name that cannot name its Secret": {
			src:  strings.Replace(goodConfig, "name: ghcr", "name: GHCR", 1),
			want: []string{"4: metadata.name names the config's Secret: lower case letters, digits and hyphens, at most 63"},
		},
		"a spec field misspelt": {
			src:  strings.Replace(goodConfig, "  alias: GitHub", "  aliass: GitHub", 1),
			want: []string{"9: ", "aliass"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diags, ok := CheckConfigFile("ghcr.yaml", []byte(tc.src), Options{ConfigTemplates: configTemplates(t), ConfigTemplatesFromCluster: tc.clusterRead})
			require.True(t, ok, "it is a Config")
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

	_, ok := CheckConfigFile("app.yaml", []byte("apiVersion: core.oam.dev/v1beta1\nkind: Application\n"), Options{})
	assert.False(t, ok, "an Application is not a Config")
}

// A config template is indexed by the name metadata gives it, without compiling it.
func TestConfigTemplateHeader(t *testing.T) {
	name, sensitive, ok := ConfigTemplateHeader("t.cue", []byte(goodConfigTemplate))
	require.True(t, ok)
	assert.Equal(t, "image-registry", name)
	assert.True(t, sensitive)
	_, _, ok = ConfigTemplateHeader("t.cue", []byte("web: {type: \"component\"}\ntemplate: output: {}\n"))
	assert.False(t, ok)
}
