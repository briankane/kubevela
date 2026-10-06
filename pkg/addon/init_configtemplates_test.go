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

package addon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/config"
)

// Every addon gets the directories the loader reads, config templates and UI
// schemas among them, whether or not it starts with samples.
func TestInitCmd_CreatesEveryDirectoryTheLoaderReads(t *testing.T) {
	for _, samples := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "my-addon")
		cmd := InitCmd{AddonName: "my-addon", Path: dir, NoSamples: !samples}
		require.NoError(t, cmd.CreateScaffold())
		for _, d := range []string{ResourcesDirName, DefinitionsDirName, ConfigTemplateDirName, DefSchemaName, ViewDirName} {
			info, err := os.Stat(filepath.Join(dir, d))
			require.NoError(t, err, "samples %v: %s", samples, d)
			assert.True(t, info.IsDir())
		}
	}
}

// The sample config template is one KubeVela parses as a config template.
func TestInitCmd_SampleConfigTemplateParses(t *testing.T) {
	restore := cuex.EnableExternalPackageForDefaultCompiler
	cuex.EnableExternalPackageForDefaultCompiler = false
	t.Cleanup(func() { cuex.EnableExternalPackageForDefaultCompiler = restore })

	dir := filepath.Join(t.TempDir(), "my-addon")
	cmd := InitCmd{AddonName: "my-addon", Path: dir}
	require.NoError(t, cmd.CreateScaffold())
	content, err := os.ReadFile(filepath.Join(dir, ConfigTemplateDirName, "my-config.cue"))
	require.NoError(t, err)

	tmpl, err := config.NewConfigFactory(nil).ParseTemplate(context.Background(), "", content)
	require.NoError(t, err)
	assert.Equal(t, "my-addon-config", tmpl.Name)
}
