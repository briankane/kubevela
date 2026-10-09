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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParameterAt(t *testing.T) {
	src := "web: {\n\ttype: \"component\"\n}\ntemplate: {\n\toutput: spec: {replicas: parameter.scale.replicas, image: parameter.image}\n\tparameter: {\n\t\timage: string\n\t\tscale: replicas: *1 | int\n\t}\n}\n"
	at := func(marker string, back int) ([]string, bool) {
		return ParameterAt("web.cue", src, strings.Index(src, marker)+len(marker)-back)
	}
	p, ok := at("\t\timage", 1)
	assert.True(t, ok)
	assert.Equal(t, []string{"image"}, p, "on the declaration")
	p, ok = at("parameter.scale.replicas", 1)
	assert.True(t, ok)
	assert.Equal(t, []string{"scale", "replicas"}, p, "on a reference, nested")
	_, ok = at("output", 1)
	assert.False(t, ok, "not a parameter")
}

const renameApps = `apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: shop
spec:
  components:
    - name: web
      type: web
      properties:
        image: nginx
        scale:
          replicas: 2
      traits:
        - type: web
          properties:
            image: not-the-component
    - name: other
      type: worker
      properties:
        image: busybox
    - name: pinned
      type: web@v2
      properties:
        "image": redis
---
apiVersion: v1
kind: ConfigMap
data:
  image: x
`

func TestPropertyRenameEdits(t *testing.T) {
	got := applyRangeEdits(renameApps, PropertyRenameEdits(renameApps, "component", "web", []string{"image"}, "picture"))
	assert.Contains(t, got, "      properties:\n        picture: nginx\n")
	assert.Contains(t, got, "        \"picture\": redis\n", "a pinned version, the key's quotes kept")
	assert.Contains(t, got, "            image: not-the-component", "a trait of the same name is another definition")
	assert.Contains(t, got, "        image: busybox", "another type")
	assert.Contains(t, got, "  image: x", "not an Application")

	nested := applyRangeEdits(renameApps, PropertyRenameEdits(renameApps, "component", "web", []string{"scale", "replicas"}, "count"))
	assert.Contains(t, nested, "        scale:\n          count: 2\n")

	traits := applyRangeEdits(renameApps, PropertyRenameEdits(renameApps, "trait", "web", []string{"image"}, "picture"))
	assert.Contains(t, traits, "            picture: not-the-component")
	assert.Contains(t, traits, "        image: nginx")
}

const renameTests = `import "vela/test"

"renders": test.#ComponentRender & {
	definition: "web"
	parameter: {
		image: "nginx"
		scale: replicas: 2
	}
	expect: output: spec: image: "nginx"
}

"another definition": test.#ComponentRender & {
	definition: "worker"
	parameter: image: "busybox"
}

"quoted": test.#ComponentRender & {
	definition: "web"
	parameter: "image": "redis"
}
`

func TestTestParameterRenameEdits(t *testing.T) {
	got := applyRangeEdits(renameTests, TestParameterRenameEdits("web_test.cue", renameTests, []string{"web"}, []string{"image"}, "picture"))
	assert.Contains(t, got, "\tparameter: {\n\t\tpicture: \"nginx\"\n")
	assert.Contains(t, got, `parameter: "picture": "redis"`, "quotes kept")
	assert.Contains(t, got, `parameter: image: "busybox"`, "another definition's case")
	assert.Contains(t, got, `expect: output: spec: image: "nginx"`, "only the parameter")

	nested := applyRangeEdits(renameTests, TestParameterRenameEdits("web_test.cue", renameTests, []string{"web"}, []string{"scale", "replicas"}, "count"))
	assert.Contains(t, nested, "scale: count: 2")
}
