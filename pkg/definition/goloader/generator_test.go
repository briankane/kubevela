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

package goloader_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/goloader"
)

const helloComponent = `package components

import "github.com/oam-dev/kubevela/pkg/definition/defkit"

func init() { defkit.Register(Hello()) }

func Hello() *defkit.ComponentDefinition {
	image := defkit.String("image").Required()
	return defkit.NewComponent("hello").
		Workload("apps/v1", "Deployment").
		Params(image).
		Template(func(tpl *defkit.Template) {
			tpl.Output(defkit.NewResource("apps/v1", "Deployment").Set("spec.template.spec.containers[0].image", image))
		})
}
`

// A file renders by its own module's go.mod, wherever the renderer runs:
// here, outside any KubeVela checkout, with KubeVela replaced by a path
// relative to the module.
func TestLoadFromFileUsesTheModulesGoMod(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	require.NoError(t, err)
	module := t.TempDir()
	rel, err := filepath.Rel(module, repo)
	require.NoError(t, err)
	goMod := "module example.com/defs\n\ngo 1.23.8\n\nrequire github.com/oam-dev/kubevela v0.0.0\n\nreplace github.com/oam-dev/kubevela => " + rel + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(module, "components"), 0750))
	file := filepath.Join(module, "components", "hello.go")
	require.NoError(t, os.WriteFile(file, []byte(helloComponent), 0600))

	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	results, err := goloader.LoadFromFile(file)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	assert.Contains(t, results[0].CUE, "hello")
}
