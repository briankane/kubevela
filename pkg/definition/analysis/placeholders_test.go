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

// A scaffold from a released vela def init marks its workload "<change me>".
const scaffolded = `"old": {
	attributes: workload: definition: {
		apiVersion: "<change me> apps/v1"
		kind:       "<change me> Deployment"
	}
	type: "component"
}
template: {
	output: {apiVersion: "apps/v1", kind: "Deployment"}
	parameter: {}
}
`

func TestScaffoldPlaceholders(t *testing.T) {
	diags := AnalyzeWith("old.cue", []byte(scaffolded), Options{}).Diagnostics
	require.Len(t, diags, 2, "one for each placeholder, and nothing else: %v", diags)
	for i, want := range []struct {
		line int
		text string
	}{{3, `"apps/v1"`}, {4, `"Deployment"`}} {
		d := diags[i]
		assert.Equal(t, SeverityError, d.Severity)
		assert.Equal(t, want.line, d.Range.Start.Line)
		assert.Contains(t, d.Message, "<change me>")
		if assert.Len(t, d.Fixes, 1) && assert.Len(t, d.Fixes[0].Edits, 1) {
			assert.Equal(t, want.text, d.Fixes[0].Edits[0].NewText, "the fix keeps what the scaffold suggested")
		}
	}
}
