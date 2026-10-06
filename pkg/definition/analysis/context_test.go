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

// The fields offered come from propexpr's registry, with its types and docs.
func TestContextFieldsComeFromTheRegistry(t *testing.T) {
	byName := func(fields []ContextField) map[string]ContextField {
		m := map[string]ContextField{}
		for _, f := range fields {
			m[f.Name] = f
		}
		return m
	}
	component := byName(ContextFields("component"))
	require.Contains(t, component, "appName")
	assert.Equal(t, "string", component["appName"].Type)
	assert.Equal(t, "The Application's name", component["appName"].Doc)
	assert.Equal(t, "int", component["appRevisionNum"].Type)
	assert.NotContains(t, component, "traitType")
	assert.False(t, component["output"].Required, "a component's own output exists only once rendered")

	trait := byName(ContextFields("trait"))
	assert.Contains(t, trait, "traitType")
	assert.True(t, trait["output"].Required, "a trait renders against its component's output")

	step := byName(ContextFields("workflow-step"))
	assert.Contains(t, step, "stepSessionID")
	assert.NotContains(t, step, "componentName")

	assert.Nil(t, ContextFields("source"), "a source's context is not modelled, so it is left open")
}
