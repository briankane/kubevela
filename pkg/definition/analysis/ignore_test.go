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

func TestIDEIgnoreSilencesAField(t *testing.T) {
	src := componentHeader + `template: {
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		// +ide:ignore
		metadata: {
			name:   context.nmae
			labels: a: parameter.nope
		}
		data: v: context.appNmae
	}
	parameter: {}
}
`
	got := lines(withoutInfo(Analyze("def.cue", []byte(src)).Diagnostics))
	assert.Len(t, got, 1, strings.Join(got, "\n"))
	assert.Contains(t, strings.Join(got, "\n"), "appNmae", "outside the ignored field, still reported")
}

func TestIDEIgnoreInTheHeader(t *testing.T) {
	src := `"x": {
	type: "component"
	// +ide:ignore
	attributes: workloadd: {}
}
template: output: {apiVersion: "v1", kind: "ConfigMap"}
`
	assert.Empty(t, lines(withoutInfo(Analyze("def.cue", []byte(src)).Diagnostics)))
}

func TestIDEIgnoreFile(t *testing.T) {
	src := "// +ide:ignore-file\n" + componentHeader + "template: output: {kind: context.nmae}\n"
	assert.Empty(t, Analyze("def.cue", []byte(src)).Diagnostics)
	broken := "// +ide:ignore-file\n" + componentHeader + "template: {\n"
	res := Analyze("def.cue", []byte(broken))
	assert.True(t, res.IsDefinition)
	assert.Empty(t, res.Diagnostics, "even a syntax error")
}
