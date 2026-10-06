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

package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func frame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func TestDefinitionLSPCommandServesOverStdio(t *testing.T) {
	restore := cuex.EnableExternalPackageForDefaultCompiler
	t.Cleanup(func() { cuex.EnableExternalPackageForDefaultCompiler = restore })
	cuex.EnableExternalPackageForDefaultCompiler = true

	var out bytes.Buffer
	cmd := NewDefinitionLSPCommand()
	cmd.SetIn(strings.NewReader(
		frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`) +
			frame(`{"jsonrpc":"2.0","method":"exit"}`)))
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--stdio"})
	require.NoError(t, cmd.Execute())

	assert.True(t, strings.HasPrefix(out.String(), "Content-Length: "), "stdout must carry only protocol messages: %q", out.String())
	assert.Contains(t, out.String(), `"name":"vela-def-lsp"`)
	assert.False(t, cuex.EnableExternalPackageForDefaultCompiler, "the server must not read packages from the cluster")
}
