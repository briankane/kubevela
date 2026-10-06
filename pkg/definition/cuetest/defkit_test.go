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

package cuetest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/goloader"
)

const renderedScaler = `"scaler": {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	patch: spec: replicas: parameter.replicas
	parameter: replicas: *1 | int
}
`

const renderedLabels = `"labels": {
	type: "trait"
}
template: {
	patch: metadata: labels: parameter
	parameter: [string]: string
}
`

// withRenderedGo makes .go definitions render as results, for the test.
func withRenderedGo(t *testing.T, results ...goloader.LoadResult) {
	t.Helper()
	old := renderGo
	renderGo = func(string) ([]goloader.LoadResult, error) { return results, nil }
	t.Cleanup(func() { renderGo = old })
}

func TestDefKitSubject(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "traits.go"), []byte("package traits\n"), 0o600))
	testFile := filepath.Join(dir, "traits_test.cue")
	scaler := goloader.LoadResult{CUE: renderedScaler, Definition: goloader.DefinitionInfo{Name: "scaler", Type: "trait"}}
	labels := goloader.LoadResult{CUE: renderedLabels, Definition: goloader.DefinitionInfo{Name: "labels", Type: "trait"}}

	t.Run("the file's only definition", func(t *testing.T) {
		withRenderedGo(t, scaler)
		s, err := (&loader{subjects: map[string]Subject{}}).subject(testFile, "traits.go")
		require.NoError(t, err)
		assert.Equal(t, "scaler", s.Name)
		assert.Equal(t, KindTrait, s.Kind)
	})
	t.Run("one of several, by name", func(t *testing.T) {
		withRenderedGo(t, scaler, labels)
		s, err := (&loader{subjects: map[string]Subject{}}).subject(testFile, "traits.go#labels")
		require.NoError(t, err)
		assert.Equal(t, "labels", s.Name)
	})
	t.Run("several, unnamed", func(t *testing.T) {
		withRenderedGo(t, scaler, labels)
		_, err := (&loader{subjects: map[string]Subject{}}).subject(testFile, "traits.go")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "traits.go#scaler")
		assert.Contains(t, err.Error(), "traits.go#labels")
	})
	t.Run("a name the file does not define", func(t *testing.T) {
		withRenderedGo(t, scaler)
		_, err := (&loader{subjects: map[string]Subject{}}).subject(testFile, "traits.go#nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nope")
	})
	t.Run("a render that fails", func(t *testing.T) {
		withRenderedGo(t, goloader.LoadResult{Definition: goloader.DefinitionInfo{Name: "scaler"}, Error: errors.New("boom")})
		_, err := (&loader{subjects: map[string]Subject{}}).subject(testFile, "traits.go")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}
