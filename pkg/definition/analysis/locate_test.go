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
	"testing"

	"github.com/stretchr/testify/assert"
)

const locateDef = `import "strconv"

boom: {
	type: "component"
}
template: {
	output: {
		apiVersion: "v1"
		data: n: "\(strconv.Atoi(parameter.count) + 1)"
	}
	if parameter.extra != _|_ {
		outputs: extra: {kind: "Service"}
	}
	parameter: {
		count:  string
		extra?: bool
	}
}
`

const locateApp = `apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: a
spec:
  components:
    - name: web
      type: svc
      properties:
        image: x
        replicas: "$(source.cfg.host)"
`

func TestLocateField(t *testing.T) {
	cases := []struct {
		name, file, src, path string
		line, col             int
	}{
		{"a field written as a shorthand chain", "boom.cue", locateDef, "template.output.data.n", 9, 9},
		{"a field inside an if", "boom.cue", locateDef, "template.outputs.extra.kind", 12, 20},
		{"a parameter", "boom.cue", locateDef, "template.parameter.count", 15, 3},
		{"the deepest part written", "boom.cue", locateDef, "template.output.metadata.name", 7, 2},
		{"an Application's property", "app.yaml", locateApp, "spec.components.0.properties.replicas", 11, 9},
	}
	for _, c := range cases {
		r, ok := LocateField(c.file, []byte(c.src), c.path)
		if assert.True(t, ok, c.name) {
			assert.Equal(t, Position{Line: c.line, Column: c.col}, r.Start, c.name)
		}
	}
	_, ok := LocateField("boom.cue", []byte(locateDef), "nothing.here")
	assert.False(t, ok, "a path whose first part is not written")
}
