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

func TestParametersWithoutUsage(t *testing.T) {
	src := stepHeader + `template: {
	parameter: {
		// +usage=Image to run
		image: string
		// Not a marker, so not a description VelaUX shows.
		port: *80 | int
		env: [...{
			// +usage=Variable name
			name:  string
			value: string
		}]
		resources: {
			// +usage=CPU request
			cpu:    string
			memory: string
		}
		labels: [string]: string
		_internal: 1
		#Probe: path: string
	}
}
`
	res := Analyze("def.cue", []byte(src))
	var infos []Diagnostic
	for _, d := range res.Diagnostics {
		require.Equal(t, SeverityInfo, d.Severity, "only recommendations expected: %d %s", d.Range.Start.Line, d.Message)
		infos = append(infos, d)
	}
	got := lines(infos)
	// port, env, env's value, resources, resources' memory, labels.
	require.Len(t, infos, 6, strings.Join(got, "\n"))
	for i, line := range []int{10, 11, 14, 16, 19, 21} {
		assert.Equal(t, line, infos[i].Range.Start.Line, got[i])
	}
	assert.Contains(t, infos[0].Message, "port has no +usage")
}

// withoutInfo leaves out recommendations, for a test about what is wrong.
func withoutInfo(diags []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Severity != SeverityInfo {
			out = append(out, d)
		}
	}
	return out
}
