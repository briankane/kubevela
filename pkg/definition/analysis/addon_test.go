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

// addonDir is an addon with metadata and a parameter, to check its files in.
func addonDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my-addon")
	for _, sub := range []string{"config-templates", "schemas", "views", "resources"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, sub), 0o750))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte("name: my-addon\nversion: 1.0.0\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "parameter.cue"), []byte("parameter: {\n\t// +usage=Image to run\n\timage: *\"nginx\" | string\n}\n"), 0o600))
	return dir
}

const goodAddonTemplate = `package main

output: {
	apiVersion: "core.oam.dev/v1beta1"
	kind:       "Application"
	spec: components: [{
		name: context.metadata.name
		type: "webservice"
		properties: image: parameter.image
	}]
}
outputs: ns: {apiVersion: "v1", kind: "Namespace", metadata: name: "x"}
`

// checkAddon checks src as the addon file at rel, and reports its errors and
// warnings as "line: message".
func checkAddon(t *testing.T, dir, rel, src string) []string {
	t.Helper()
	diags, ok := CheckAddonFile(filepath.Join(dir, rel), []byte(src), Options{})
	require.True(t, ok, "%s is an addon file", rel)
	var out []string
	for _, d := range withoutInfo(diags) {
		out = append(out, strconv.Itoa(d.Range.Start.Line)+": "+d.Message)
	}
	return out
}

func TestAddonTemplate(t *testing.T) {
	dir := addonDir(t)
	cases := map[string]struct {
		src  string
		want []string
	}{
		"a valid template": {src: goodAddonTemplate},
		"output is not an Application": {
			src:  strings.Replace(goodAddonTemplate, `kind:       "Application"`, `kind:       "Deployment"`, 1),
			want: []string{"5: ", "Application"},
		},
		"an Application field misspelt": {
			src:  strings.Replace(goodAddonTemplate, "spec: components:", "spec: componets:", 1),
			want: []string{"6: ", "componets"},
		},
		"a component with no type": {
			src:  strings.Replace(goodAddonTemplate, "\t\ttype: \"webservice\"\n", "", 1),
			want: []string{"type"},
		},
		"no output": {
			src:  "package main\n\noutputs: {}\n",
			want: []string{"output"},
		},
		"a KubeVela package": {
			src:  "package main\n\nimport \"vela/kube\"\n\n" + strings.TrimPrefix(goodAddonTemplate, "package main\n\n"),
			want: []string{"3: ", "vela/kube"},
		},
		"a context typo": {
			src:  strings.Replace(goodAddonTemplate, "context.metadata.name", "context.metadata.nam", 1),
			want: []string{"7: ", "nam"},
		},
		"a parameter typo": {
			src:  strings.Replace(goodAddonTemplate, "parameter.image", "parameter.imag", 1),
			want: []string{"9: ", "imag"},
		},
		"an auxiliary output that is not an object": {
			src:  strings.Replace(goodAddonTemplate, `outputs: ns: {apiVersion: "v1", kind: "Namespace", metadata: name: "x"}`, `outputs: ns: {metadata: name: "x"}`, 1),
			want: []string{"12: ", "apiVersion"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkAddon(t, dir, "template.cue", tc.src)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got, "want %v", tc.want)
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}

const goodConfigTemplate = `import "strings"

metadata: {
	name:        "image-registry"
	alias:       "Image Registry"
	description: "Credentials for a registry"
	scope:       "system"
	sensitive:   true
}
template: {
	parameter: {
		registry: string
		token?:   string
	}
	output: {
		type: "kubernetes.io/dockerconfigjson"
		stringData: registry: strings.ToLower(parameter.registry)
	}
}
`

func TestAddonConfigTemplate(t *testing.T) {
	dir := addonDir(t)
	cases := map[string]struct {
		src  string
		want []string
	}{
		"a valid config template": {src: goodConfigTemplate},
		"a scope that is not one": {
			src:  strings.Replace(goodConfigTemplate, `"system"`, `"cluster"`, 1),
			want: []string{"7: ", "scope"},
		},
		"a metadata key misspelt": {
			src:  strings.Replace(goodConfigTemplate, "alias:", "alais:", 1),
			want: []string{"5: ", "alais"},
		},
		"no parameter": {
			src:  "metadata: name: \"x\"\ntemplate: output: type: \"Opaque\"\n",
			want: []string{"parameter"},
		},
		"no metadata": {
			src:  "template: parameter: a: string\n",
			want: []string{"metadata"},
		},
		"output is not a Secret": {
			src:  strings.Replace(goodConfigTemplate, "stringData:", "stringDat:", 1),
			want: []string{"17: ", "stringDat"},
		},
		"a context typo": {
			src:  strings.Replace(goodConfigTemplate, "ToLower(parameter.registry)", "ToLower(context.nam)", 1),
			want: []string{"17: ", "nam"},
		},
		"a KubeVela package other than vela/config": {
			src:  "import \"vela/kube\"\n\n" + strings.TrimPrefix(goodConfigTemplate, "import \"strings\"\n\n"),
			want: []string{"1: ", "vela/kube"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkAddon(t, dir, "config-templates/registry.cue", tc.src)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got, "want %v", tc.want)
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}

const goodUISchema = `- jsonKey: image
  uiType: ImageInput
  sort: 1
  validate:
    required: true
    immutable: true
  style:
    colSpan: 12
- jsonKey: ports
  sort: 3
  disable: true
  conditions:
  - jsonKey: expose
    op: "=="
    value: true
    action: enable
  subParameters:
  - jsonKey: port
    validate:
      min: 1
      options:
      - label: HTTP
        value: 80
`

func TestAddonUISchema(t *testing.T) {
	dir := addonDir(t)
	cases := map[string]struct {
		file, src string
		want      []string
	}{
		"a valid ui schema": {file: "component-uischema-webservice.yaml", src: goodUISchema},
		"the short name is not read from an addon": {
			file: "trait-scaler.yaml",
			src:  goodUISchema,
			want: []string{"trait-uischema-scaler"},
		},
		"a key misspelt": {
			file: "component-uischema-webservice.yaml",
			src:  strings.Replace(goodUISchema, "uiType:", "uitype:", 1),
			want: []string{"2: ", "uitype"},
		},
		"a nested key misspelt": {
			file: "component-uischema-webservice.yaml",
			src:  strings.Replace(goodUISchema, "      min: 1", "      minimum: 1", 1),
			want: []string{"20: ", "minimum"},
		},
		"a condition op that is not one": {
			file: "component-uischema-webservice.yaml",
			src:  strings.Replace(goodUISchema, `op: "=="`, `op: "<"`, 1),
			want: []string{"14: ", "op"},
		},
		"a wrong type": {
			file: "component-uischema-webservice.yaml",
			src:  strings.Replace(goodUISchema, "sort: 1", "sort: first", 1),
			want: []string{"3: ", "sort"},
		},
		"a name KubeVela does not read": {
			file: "webservice.yaml",
			src:  goodUISchema,
			want: []string{"<type>-uischema-<name>"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := checkAddon(t, dir, "schemas/"+tc.file, tc.src)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			require.NotEmpty(t, got, "want %v", tc.want)
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}

func TestAddonView(t *testing.T) {
	dir := addonDir(t)
	good := "import \"vela/ql\"\n\nparameter: name: string\nresources: ql.#ListResourcesInApp & {app: name: parameter.name}\nstatus: resources.list\n"
	assert.Empty(t, checkAddon(t, dir, "views/app-resources.cue", good))
	assert.Empty(t, checkAddon(t, dir, "views/app-resources.cue", strings.Replace(good, "status:", "export:", 1)))
	got := checkAddon(t, dir, "views/app-resources.cue", strings.Replace(good, "status:", "result:", 1))
	assert.Contains(t, strings.Join(got, "\n"), "status")
}

func TestNotAnAddonFile(t *testing.T) {
	dir := addonDir(t)
	_, ok := CheckAddonFile(filepath.Join(dir, "definitions", "x.cue"), []byte("x: 1"), Options{})
	assert.False(t, ok, "definitions are checked as definitions")
	_, ok = CheckAddonFile(filepath.Join(t.TempDir(), "template.cue"), []byte("x: 1"), Options{})
	assert.False(t, ok, "not in an addon")
	root, ok := AddonRoot(filepath.Join(dir, "schemas", "a.yaml"))
	assert.True(t, ok)
	assert.Equal(t, dir, root)
}
