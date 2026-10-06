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

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
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
		"a metadata key misspelt, which is ignored": {
			src:  strings.Replace(goodConfigTemplate, "alias:", "alais:", 1),
			want: []string{"5: KubeVela does not read metadata.alais"},
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
		"a config template's form":  {file: "config-uischema-image-registry.yaml", src: goodUISchema},
		"an addon's parameter form": {file: "addon-uischema-my-addon.yaml", src: goodUISchema},
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
	assert.Empty(t, checkAddon(t, dir, "views/app-resources.cue", strings.Replace(good, "status:", "result:", 1)), "a query may name the field it exports")
	diags, _ := CheckAddonFile(filepath.Join(dir, "views", "app-resources.cue"), []byte(strings.Replace(good, "status:", "result:", 1)), Options{})
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityInfo, diags[0].Severity)
	expectAddon(t, dir, "views/app-resources.cue", strings.Replace(good, "status:", "export: \"result\"\nresults:", 1), "5: ", "result")
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

// expectAddon checks src as the addon file at rel and asserts each want
// appears in what it reports, or that nothing is, when want is empty.
func expectAddon(t *testing.T, dir, rel, src string, want ...string) {
	t.Helper()
	got := checkAddon(t, dir, rel, src)
	if len(want) == 0 {
		assert.Empty(t, got)
		return
	}
	require.NotEmpty(t, got, "want %v", want)
	for _, w := range want {
		assert.Contains(t, strings.Join(got, "\n"), w)
	}
}

const goodMetadata = `name: my-addon
version: 1.0.0
description: An addon
icon: https://example.com/icon.png
tags:
  - demo
deployTo:
  runtimeCluster: true
dependencies:
  - name: fluxcd
system:
  vela: ">=1.9.0"
  kubernetes: ">=1.24"
needNamespace:
  - flux-system
invisible: false
`

func TestAddonMetadata(t *testing.T) {
	dir := addonDir(t)
	expectAddon(t, dir, "metadata.yaml", goodMetadata)
	expectAddon(t, dir, "metadata.yaml", strings.Replace(goodMetadata, "tags:", "tag:", 1), "5: ", "tag")
	diags, _ := CheckAddonFile(filepath.Join(dir, "metadata.yaml"), []byte(strings.Replace(goodMetadata, "tags:", "tag:", 1)), Options{})
	require.NotEmpty(t, diags)
	assert.Equal(t, SeverityWarning, diags[0].Severity, "KubeVela drops a key it does not know, so it is a warning")
	expectAddon(t, dir, "metadata.yaml", strings.Replace(goodMetadata, "runtimeCluster: true", "runtimeCluster: yes please", 1), "8: ", "runtimeCluster")
	expectAddon(t, dir, "metadata.yaml", strings.Replace(goodMetadata, "version: 1.0.0\n", "", 1), "version")
	expectAddon(t, dir, "metadata.yaml", strings.Replace(goodMetadata, "  vela:", "  velaa:", 1), "12: KubeVela does not read system.velaa")
	expectAddon(t, dir, "metadata.cue", "name: \"x\"\n", "metadata.yaml")
}

func TestAddonResources(t *testing.T) {
	dir := addonDir(t)
	t.Run("yaml objects", func(t *testing.T) {
		good := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  k: v\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: b\n"
		expectAddon(t, dir, "resources/objects.yaml", good)
		expectAddon(t, dir, "resources/objects.yaml", strings.Replace(good, "kind: Namespace\n", "", 1), "8: ", "kind")
		expectAddon(t, dir, "resources/objects.yaml", strings.Replace(good, "  name: a\n", "", 1), "1: ", "metadata.name")
	})
	t.Run("yaml objects against their kinds", func(t *testing.T) {
		kinds, err := kubeschema.Builtin()
		require.NoError(t, err)
		src := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndatta:\n  k: v\n"
		diags, ok := CheckAddonFile(filepath.Join(dir, "resources", "cm.yaml"), []byte(src), Options{Kinds: kinds})
		require.True(t, ok)
		require.NotEmpty(t, diags)
		assert.Equal(t, 5, diags[0].Range.Start.Line, diags[0].Message)
		assert.Contains(t, diags[0].Message, "datta")
		diags, _ = CheckAddonFile(filepath.Join(dir, "resources", "cm.yaml"), []byte(src), Options{})
		assert.Empty(t, withoutInfo(diags), "no kinds, no schema check")
	})
	t.Run("a component of its own", func(t *testing.T) {
		good := "output: {\n\tname: \"extra\"\n\ttype: \"k8s-objects\"\n\tproperties: objects: [{apiVersion: \"v1\", kind: \"Namespace\", metadata: name: context.metadata.name}]\n}\n"
		expectAddon(t, dir, "resources/extra.cue", good)
		expectAddon(t, dir, "resources/extra.cue", strings.Replace(good, "\ttype: \"k8s-objects\"\n", "", 1), "type")
		expectAddon(t, dir, "resources/extra.cue", strings.Replace(good, "properties:", "propertes:", 1), "4: output.propertes")
		expectAddon(t, dir, "resources/extra.cue", "x: 1\n", "output")
	})
	t.Run("part of the template", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "template.cue"), []byte(goodAddonTemplate), 0o600))
		good := "package main\n\n_shared: image: parameter.image\n"
		expectAddon(t, dir, "resources/shared.cue", good)
		expectAddon(t, dir, "resources/shared.cue", strings.Replace(good, "parameter.image", "parameter.imge", 1), "3: ", "imge")
	})
}

func TestAddonTemplateYAML(t *testing.T) {
	dir := addonDir(t)
	good := "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: my-addon\nspec:\n  components:\n  - name: a\n    type: webservice\n"
	expectAddon(t, dir, "template.yaml", good)
	expectAddon(t, dir, "template.yaml", strings.Replace(good, "components:", "componets:", 1), "6: ", "componets")
	expectAddon(t, dir, "template.yaml", strings.Replace(good, "    type: webservice\n", "", 1), "type")
	expectAddon(t, dir, "template.yaml", strings.Replace(good, "kind: Application", "kind: Deployment", 1), "2: ", "Application")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "template.cue"), []byte(goodAddonTemplate), 0o600))
	expectAddon(t, dir, "template.yaml", good, "template.cue")
}

func TestAddonParameterAndNotes(t *testing.T) {
	dir := addonDir(t)
	expectAddon(t, dir, "parameter.cue", "parameter: {\n\timage: *\"nginx\" | string\n}\nconst: x: 1\n")
	expectAddon(t, dir, "parameter.cue", "params: image: string\n", "parameter")
	expectAddon(t, dir, "parameter.cue", "parameter: image: string & 1\n", "1: ")
	expectAddon(t, dir, "NOTES.cue", "notes: \"Installed \\(parameter.image) on \\(context.installer.cluster)\"\n")
	diags, ok := CheckAddonFile(filepath.Join(dir, "NOTES.cue"), []byte("info: \"x\"\n"), Options{})
	require.True(t, ok)
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity, "notes never fail an install")
	assert.Contains(t, diags[0].Message, "notes")
}

func TestAddonNestedResources(t *testing.T) {
	dir := addonDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resources", "components", "deep"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "resources", "components", "deep", "shared.cue"), []byte("package main\n\n_controller: {name: \"c\", type: \"webservice\"}\n"), 0o600))
	tmpl := "package main\n\noutput: {apiVersion: \"core.oam.dev/v1beta1\", kind: \"Application\", spec: components: [_controller]}\n"
	expectAddon(t, dir, "template.cue", tmpl)
	expectAddon(t, dir, "resources/components/deep/objects.yaml", "apiVersion: v1\nkind: Namespace\n", "metadata.name")
	expectAddon(t, dir, "resources/components/deep/objects.yaml", "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n")
}
