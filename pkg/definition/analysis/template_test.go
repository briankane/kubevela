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
	"github.com/stretchr/testify/require"
)

func TestTemplateSource(t *testing.T) {
	src := `import "strings"

` + componentHeader + `template: {
	output: metadata: name: strings.ToLower(context.name)
	parameter: replicas: *1 | int
}
`
	tmpl, ok := TemplateSource("my-worker.cue", []byte(src))
	require.True(t, ok)
	assert.Equal(t, "my-worker", tmpl.Name)
	assert.Equal(t, "component", tmpl.Type)
	assert.Equal(t, `import "strings"

output: metadata: name: strings.ToLower(context.name)
parameter: replicas: *1 | int
`, tmpl.Body, "the body is stored as the controller stores spec.schematic.cue.template")
}

func TestTemplateSourceOfNonDefinitions(t *testing.T) {
	_, ok := TemplateSource("values.cue", []byte("a: 1\n"))
	assert.False(t, ok)
	_, ok = TemplateSource("broken.cue", []byte(componentHeader+"template: {\n"))
	assert.False(t, ok, "a file that does not parse has no template to render")
}
