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
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// builtin_definitions.json.gz bundles KubeVela's own definitions, so an
// Application naming one is checked without a cluster. Run
// `go test ./pkg/definition/analysis -run TestBuiltinDefinitionsMatchTheTemplates -update`
// after they change.
func TestBuiltinDefinitionsMatchTheTemplates(t *testing.T) {
	got, err := bundleDefinitions(filepath.Join("..", "..", "..", "vela-templates", "definitions", "internal"))
	require.NoError(t, err)
	if *update {
		require.NoError(t, os.WriteFile(builtinDefinitionsFile, got, 0o600))
	}
	want, err := os.ReadFile(builtinDefinitionsFile)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(unzip(t, got), unzip(t, want)), "%s is stale: rerun this test with -update", builtinDefinitionsFile)
}

func unzip(t *testing.T, data []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	var b bytes.Buffer
	_, err = b.ReadFrom(zr)
	require.NoError(t, err)
	return b.Bytes()
}

// bundleDefinitions gathers the CUE definitions under dir, by path, as
// gzipped JSON, in a stable order.
func bundleDefinitions(dir string) ([]byte, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".cue") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = string(src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([][2]string, 0, len(keys))
	for _, k := range keys {
		ordered = append(ordered, [2]string{k, files[k]})
	}
	data, err := json.Marshal(ordered)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func TestBuiltinDefinitions(t *testing.T) {
	defs := BuiltinDefinitions()
	byKey := map[string]AppDefinition{}
	for _, d := range defs {
		byKey[d.Type+"/"+d.Name] = d
	}
	require.Contains(t, byKey, "component/webservice")
	require.Contains(t, byKey, "trait/scaler")
	require.Contains(t, byKey, "policy/topology")
	require.Contains(t, byKey, "workflow-step/deploy")
	assert.Equal(t, SourceBuiltin, byKey["component/webservice"].Source)
	assert.NotEmpty(t, byKey["component/webservice"].Description)
}
