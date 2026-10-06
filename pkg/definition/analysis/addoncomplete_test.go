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

// completeAddon completes doc, the addon file at rel, at the "|" in it.
func completeAddon(t *testing.T, dir, rel, doc string) map[string]Completion {
	t.Helper()
	cursor := strings.Index(doc, "|")
	require.GreaterOrEqual(t, cursor, 0)
	doc = doc[:cursor] + doc[cursor+1:]
	got, ok := CompleteAddonFile(filepath.Join(dir, rel), doc, cursor, Options{})
	require.True(t, ok, "%s is an addon file", rel)
	out := map[string]Completion{}
	for _, c := range got {
		out[c.Label] = c
	}
	return out
}

func labelsOfCompletions(m map[string]Completion) []string {
	var out []string
	for l := range m {
		out = append(out, l)
	}
	return out
}

func TestCompleteAddonTemplate(t *testing.T) {
	dir := addonDir(t)
	const head = "package main\n\n"

	t.Run("the addon's parameters", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"output: spec: components: [{name: parameter.|}]\n")
		require.Contains(t, got, "image")
		assert.Contains(t, got["image"].Doc, "Image to run")
		assert.Contains(t, got, "clusters", "--clusters adds it")
	})
	t.Run("in the middle of a word", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"output: spec: components: [{name: parameter.|image}]\n")
		assert.Contains(t, got, "image")
	})
	t.Run("the addon's metadata", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"output: metadata: name: context.metadata.n|\n")
		assert.Equal(t, []string{"name", "needNamespace"}, sortedKeys(got))
	})
	t.Run("the Application's fields", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"output: {\n\tapiVersion: \"core.oam.dev/v1beta1\"\n\tkind: \"Application\"\n\tspec: {\n\t\t|\n\t}\n}\n")
		assert.Contains(t, got, "components")
		assert.Contains(t, got, "policies")
		assert.Contains(t, got, "workflow")
	})
	t.Run("a component's fields", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"output: spec: components: [{\n\tname: \"x\"\n\tty|\n}]\n")
		assert.Equal(t, []string{"type"}, sortedKeys(got))
	})
	t.Run("what a template sets", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"out|\n")
		assert.Equal(t, []string{"output", "outputs"}, sortedKeys(got))
	})
	t.Run("no KubeVela packages to import", func(t *testing.T) {
		got := completeAddon(t, dir, "template.cue", head+"import \"vela/|\"\n")
		assert.Empty(t, got)
	})
}

func TestCompleteAddonOtherFiles(t *testing.T) {
	dir := addonDir(t)
	t.Run("a component of its own", func(t *testing.T) {
		got := completeAddon(t, dir, "resources/extra.cue", "output: {\n\tname: \"extra\"\n\t|\n}\n")
		assert.Contains(t, got, "type")
		assert.Contains(t, got, "traits")
	})
	t.Run("a config template's context", func(t *testing.T) {
		got := completeAddon(t, dir, "config-templates/reg.cue", "metadata: name: \"x\"\ntemplate: {\n\tparameter: a: string\n\toutput: stringData: n: context.|\n}\n")
		assert.Contains(t, got, "name")
		assert.Contains(t, got, "namespace")
	})
	t.Run("a config template's metadata", func(t *testing.T) {
		got := completeAddon(t, dir, "config-templates/reg.cue", "metadata: {\n\tname: \"x\"\n\t|\n}\ntemplate: parameter: {}\n")
		assert.Contains(t, got, "scope")
		assert.Contains(t, got, "sensitive")
	})
	t.Run("a view's packages", func(t *testing.T) {
		got := completeAddon(t, dir, "views/v.cue", "import \"vela/ql\"\n\nr: ql.#Li|\nstatus: r\n")
		assert.Contains(t, got, "#ListResourcesInApp")
	})
	t.Run("what NOTES.cue sets", func(t *testing.T) {
		got := completeAddon(t, dir, "NOTES.cue", "no|\n")
		assert.Equal(t, []string{"notes"}, sortedKeys(got))
	})
	_, ok := CompleteAddonFile(filepath.Join(dir, "definitions", "x.cue"), "x: 1\n", 0, Options{})
	assert.False(t, ok, "definitions complete as definitions")
}

func TestHoverAddonFile(t *testing.T) {
	dir := addonDir(t)
	doc := "package main\n\noutput: spec: components: [{name: parameter.image}]\n"
	h, ok := HoverAddonFile(filepath.Join(dir, "template.cue"), doc, strings.Index(doc, "image")+2, Options{})
	require.True(t, ok)
	assert.Contains(t, h, "image: string")
	assert.Contains(t, h, "Image to run")
}

func sortedKeys(m map[string]Completion) []string {
	out := labelsOfCompletions(m)
	sort.Strings(out)
	return out
}

// Completing where nothing is declared offers nothing, rather than failing.
func TestCompleteAddonNowhere(t *testing.T) {
	dir := addonDir(t)
	assert.Empty(t, completeAddon(t, dir, "template.cue", "package main\n\nx: nothing.here.|\n"))
	assert.Empty(t, completeAddon(t, dir, "template.cue", "package main\n\nnope: {\n\tdeeper: {\n\t\t|\n\t}\n}\n"))
}
