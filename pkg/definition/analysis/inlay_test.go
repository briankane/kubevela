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
	"github.com/stretchr/testify/require"
)

func TestInlayHints(t *testing.T) {
	hints := InlayHints("def.cue", navDoc, Options{})
	require.Len(t, hints, 1, "only replicas has a default: %v", hints)
	h := hints[0]
	assert.Equal(t, "= 1", h.Label)
	line := strings.Split(navDoc, "\n")[h.Position.Line-1]
	assert.Equal(t, "parameter.replicas", line[h.Position.Column-1-len("parameter.replicas"):h.Position.Column-1], "after the read")

	src := strings.Replace(navDoc, "replicas: *1 | int", `replicas: *1 | int
		mode: *"fast" | "safe"
		flags: *["a"] | [...string]`, 1)
	src = strings.Replace(src, "data: t: tag", `data: {t: tag, m: parameter.mode, f: parameter.flags[0]}`, 1)
	var labels []string
	for _, h := range InlayHints("def.cue", src, Options{}) {
		labels = append(labels, h.Label)
	}
	assert.ElementsMatch(t, []string{"= 1", `= "fast"`, `= ["a"]`}, labels)
}
