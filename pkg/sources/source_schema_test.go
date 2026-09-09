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

package sources

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The extracted schema was memoised in an unbounded map keyed by the whole
// template, so every edit to every definition retained another copy of its text
// for the life of the process.
func TestSchemaExprCacheIsBounded(t *testing.T) {
	for i := 0; i < schemaExprCacheSize*2; i++ {
		_, err := extractSourceSchemaExpr(fmt.Sprintf("schema: {v%d: string}\noutput: {v%d: \"x\"}\n", i, i))
		require.NoError(t, err)
	}
	require.LessOrEqual(t, schemaExprCache.Len(), schemaExprCacheSize)
}
