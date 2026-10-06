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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultTrap = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_tag: parameter.tag
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		data: {
			image: parameter.image | *"nginx"
			tag:   _tag | *"latest"
			ok:    *parameter.image | "nginx"
		}
	}
	parameter: {
		image?: string
		tag?:   string
		replicas: *1 | int
		mode: *"fast" | "safe"
	}
}
`

func TestDefaultThatAlwaysWins(t *testing.T) {
	var got []Diagnostic
	for _, d := range Analyze("def.cue", []byte(defaultTrap)).Diagnostics {
		if strings.Contains(d.Message, "default") && strings.Contains(d.Message, "wins") {
			got = append(got, d)
		}
	}
	require.Len(t, got, 2, "the reads with a literal default, not the parameter declarations: %v", got)
	assert.Equal(t, 11, got[0].Range.Start.Line)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Equal(t, 12, got[1].Range.Start.Line)

	fixed := fix(t, defaultTrap, "even when parameter.image is set", `Change to *parameter.image | "nginx"`)
	assert.Contains(t, fixed, `image: *parameter.image | "nginx"`)
}
