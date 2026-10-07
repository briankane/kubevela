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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const navDoc = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_labels: {app: context.name}
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: labels: _labels
		spec: {
			replicas: parameter.replicas
			template: spec: containers: [{image: parameter.image, name: "web"}]
		}
	}
	let tag = parameter.image
	outputs: tagged: {apiVersion: "v1", kind: "ConfigMap", data: t: tag}
	parameter: {
		// +usage=The image to run
		image: string
		replicas: *1 | int
	}
}
`

// offsetOf is the offset of the n-th (from 1) occurrence of word in doc,
// plus within.
func offsetOf(t *testing.T, doc, word string, n, within int) int {
	t.Helper()
	at := -1
	for i := 0; i < n; i++ {
		next := strings.Index(doc[at+1:], word)
		require.GreaterOrEqual(t, next, 0, word)
		at += next + 1
	}
	return at + within
}

func TestDeclaration(t *testing.T) {
	loc, ok := Declaration("def.cue", navDoc, offsetOf(t, navDoc, "parameter.image", 1, len("parameter.")+1))
	require.True(t, ok)
	assert.Equal(t, 20, loc.Range.Start.Line, "the image parameter's declaration")
	assert.Equal(t, "def.cue", loc.Path)

	loc, ok = Declaration("def.cue", navDoc, offsetOf(t, navDoc, "parameter.replicas", 1, 2))
	require.True(t, ok)
	assert.Equal(t, 18, loc.Range.Start.Line, "parameter itself")

	loc, ok = Declaration("def.cue", navDoc, offsetOf(t, navDoc, "labels: _labels", 1, len("labels: ")+1))
	require.True(t, ok)
	assert.Equal(t, 6, loc.Range.Start.Line, "a helper")

	loc, ok = Declaration("def.cue", navDoc, offsetOf(t, navDoc, "t: tag", 1, len("t: ")+1))
	require.True(t, ok)
	assert.Equal(t, 16, loc.Range.Start.Line, "a let")

	_, ok = Declaration("def.cue", navDoc, offsetOf(t, navDoc, "context.name", 1, 2))
	assert.False(t, ok, "context is KubeVela's, declared nowhere in the file")
}

func TestReferencesAndRename(t *testing.T) {
	refs, ok := References("def.cue", navDoc, offsetOf(t, navDoc, "image: string", 1, 1))
	require.True(t, ok)
	var lines []int
	for _, r := range refs {
		lines = append(lines, r.Start.Line)
	}
	assert.ElementsMatch(t, []int{13, 16, 20}, lines, "both reads and the declaration")

	edits, err := RenameEdits("def.cue", navDoc, offsetOf(t, navDoc, "parameter.image", 2, len("parameter.")+1), "img")
	require.NoError(t, err)
	renamed := applyRangeEdits(navDoc, edits)
	assert.Contains(t, renamed, "image: parameter.img,")
	assert.Contains(t, renamed, "let tag = parameter.img")
	assert.Contains(t, renamed, "\t\timg: string")
	assert.NotContains(t, renamed, "parameter.image")
	assert.Contains(t, renamed, "{image: parameter.img", "a field of the same name elsewhere is left alone")

	_, err = RenameEdits("def.cue", navDoc, offsetOf(t, navDoc, "image: string", 1, 1), "not valid")
	assert.Error(t, err)
}

func TestDeclarationInAddonFiles(t *testing.T) {
	dir := addonDir(t)
	doc := "package main\n\noutput: spec: components: [{name: parameter.image}]\n"
	loc, ok := Declaration(filepath.Join(dir, "template.cue"), doc, strings.Index(doc, "image")+1)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "parameter.cue"), loc.Path)
	assert.Equal(t, 3, loc.Range.Start.Line)
}

// applyRangeEdits applies edits, given by line and column from 1, to doc.
func applyRangeEdits(doc string, edits []RangeEdit) string {
	lines := strings.Split(doc, "\n")
	offset := func(p Position) int {
		n := 0
		for i := 0; i < p.Line-1; i++ {
			n += len(lines[i]) + 1
		}
		return n + p.Column - 1
	}
	type span struct {
		start, end int
		text       string
	}
	var spans []span
	for _, e := range edits {
		spans = append(spans, span{offset(e.Range.Start), offset(e.Range.End), e.NewText})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	out := doc
	for i := len(spans) - 1; i >= 0; i-- {
		out = out[:spans[i].start] + spans[i].text + out[spans[i].end:]
	}
	return out
}

const comprehensionDoc = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {
		apiVersion: "v1"
		kind:       "Service"
		spec: ports: [for v in parameter.ports {port: v.port, name: v.name}]
		spec: selector: {for k, l in parameter.labels {(k): l.value}}
		metadata: annotations: {for p in parameter.probes {"\(p.path)": "x"}}
	}
	let first = parameter.ports[0]
	let cfg = parameter.config
	outputs: cm: {apiVersion: "v1", kind: "ConfigMap", data: x: cfg.key}
	parameter: {
		ports: [...{
			port: int
			name: string
		}]
		labels: [string]: {value: string}
		#Probe: {path: string}
		probes: [...#Probe]
		config: {key: string}
	}
}
`

// A comprehension's variable, or a let's, leads through the value it stands
// for: v.port to port in the element of parameter.ports.
func TestDeclarationThroughComprehensions(t *testing.T) {
	cases := map[string]struct {
		word         string
		n, within    int
		line, column int
	}{
		"a list element's field":          {word: "v.port", within: 2, line: 18, column: 4},
		"another":                         {word: "v.name", within: 2, line: 19, column: 4},
		"a map value's field":             {word: "l.value", within: 2, line: 21, column: 22},
		"a field of a definition element": {word: "p.path", within: 2, line: 22, column: 12},
		"a let's field":                   {word: "cfg.key", within: 4, line: 24, column: 12},
		"the variable itself":             {word: "v.port", within: 0, line: 9, column: 21},
	}
	for name, c := range cases {
		loc, ok := Declaration("web.cue", comprehensionDoc, offsetOf(t, comprehensionDoc, c.word, max(c.n, 1), c.within))
		if !assert.True(t, ok, name) {
			continue
		}
		assert.Equal(t, []int{c.line, c.column}, []int{loc.Range.Start.Line, loc.Range.Start.Column}, name)
	}
}
