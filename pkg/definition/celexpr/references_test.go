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

package celexpr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A bare root identifier was not reported as a reference, so root validation had
// nothing to refuse: `$(source)` passed on a surface that offers no source at
// all and was then evaluated against an empty map.
func TestReferencesReportsBareRoots(t *testing.T) {
	env, err := DynEnv()
	require.NoError(t, err)

	for expr, want := range map[string]string{
		"source":          "source",
		"context":         "context",
		"source.cfg":      "source.cfg",
		"source.cfg.host": "source.cfg.host",
	} {
		refs, err := References(env, expr)
		require.NoError(t, err)
		require.Len(t, refs, 1, "expr %q", expr)
		require.Equal(t, want, refs[0].String())
	}

	require.Error(t, ValidateTree("$(source)", "context"),
		"a bare source must be refused where the surface offers none")
	require.NoError(t, ValidateTree("$(source)", "context", "source"))
}
