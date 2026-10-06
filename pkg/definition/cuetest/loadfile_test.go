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

package cuetest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// LoadFile reads the text it is given, as an editor's unsaved buffer, and
// resolves definitions beside the file it names.
func TestLoadFileUsesTheGivenText(t *testing.T) {
	r := require.New(t)
	file := "testdata/defs/scaler_test.cue"
	src, err := os.ReadFile(file)
	r.NoError(err)
	src = append(src, []byte(`
"added in the editor": test.#TraitRender & {
	definition: "scaler"
	parameter: replicas: 4
	workload: _workload
	expect: output: spec: replicas: 4
}
`)...)

	s := LoadFile(file, src)
	r.NoError(s.Err)
	r.Equal(file, s.File)
	last := s.Cases[len(s.Cases)-1]
	r.Equal("added in the editor", last.Name)
	r.Equal(countLines(src)-5, last.Line)
	r.Equal("scaler", last.Subject.Name)
	r.Nil(last.Run())
}

func TestLoadFileReportsWhyItFailed(t *testing.T) {
	s := LoadFile("testdata/defs/scaler_test.cue", []byte(`import "vela/test"

"x": test.#TraitRender & {
	definition: "no-such-def"
	expect: output: {}
}
`))
	require.Error(t, s.Err)
	require.Contains(t, s.Err.Error(), "no-such-def")
	require.Empty(t, s.Cases)
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
