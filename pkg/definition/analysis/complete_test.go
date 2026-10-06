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

func labels(cs []Completion) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Label)
	}
	return out
}

func TestCompleteMarkers(t *testing.T) {
	t.Run("every marker after a plus in a comment", func(t *testing.T) {
		cs := CompleteMarker("\t\t// +")
		assert.Contains(t, labels(cs), "+usage")
		assert.Contains(t, labels(cs), "+ui:colSpan")
		assert.Contains(t, labels(cs), "+patchStrategy")
		for _, c := range cs {
			assert.NotEmpty(t, c.Doc, c.Label)
			assert.Equal(t, 0, c.Replace, "nothing typed after the plus yet")
		}
	})
	t.Run("narrowed by what is typed", func(t *testing.T) {
		cs := CompleteMarker("// +us")
		assert.Equal(t, []string{"+usage"}, labels(cs))
		assert.Equal(t, "usage=", cs[0].Insert, "a marker that takes a value is inserted ready for it")
		assert.Equal(t, 2, cs[0].Replace, "replaces what was typed after the plus")
	})
	t.Run("a bare marker is inserted bare", func(t *testing.T) {
		cs := CompleteMarker("// +imm")
		assert.Equal(t, []string{"+immutable"}, labels(cs))
		assert.Equal(t, "immutable", cs[0].Insert)
	})
	t.Run("ui keys after ui:", func(t *testing.T) {
		cs := CompleteMarker("// +ui:col")
		assert.Equal(t, []string{"+ui:colSpan"}, labels(cs))
		assert.Equal(t, 6, cs[0].Replace)
	})
	t.Run("the values a marker takes", func(t *testing.T) {
		cs := CompleteMarker("\t// +patchStrategy=re")
		assert.Equal(t, []string{"replace", "retainKeys"}, labels(cs))
		assert.Equal(t, 2, cs[0].Replace)
		assert.Equal(t, []string{"clusters", "configs:", "envs"}, labels(CompleteMarker("// +ui:optionsFrom=")))
		assert.Equal(t, []string{"table"}, labels(CompleteMarker("// +ui:format=")))
	})
	t.Run("nothing outside a marker", func(t *testing.T) {
		assert.Empty(t, CompleteMarker("\tname: +"))
		assert.Empty(t, CompleteMarker("// a + b"))
		assert.Empty(t, CompleteMarker("// +usage=How many"))
	})
}
