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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocateFailures(t *testing.T) {
	suites, err := Load("testdata/defs/web_test.cue")
	require.NoError(t, err)
	cases := map[string]*Case{}
	for _, c := range suites[0].Cases {
		cases[c.Name] = c
	}

	for name, tc := range map[string]struct {
		failure      string
		line, column int
	}{
		// The expected field the failure names.
		"fails on purpose": {"output.spec.replicas: expected 2, got 1", 28, 24},
		// A field the expectation does not have: the deepest one it does.
		"closed on purpose":                 {"output.spec.template: unexpected field, got {}", 66, 18},
		"expects an error that never comes": {`error: expected an error matching =~"image", got none`, 34, 17},
		// A failure that names no expected field is placed at its case.
		"defaults to one replica": {"passes as written: remove @upgrade", 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			c := cases[name]
			require.NotNil(t, c)
			line, column := c.Locate(tc.failure)
			if tc.line == 0 {
				require.Equal(t, c.Line, line)
				require.Equal(t, 0, column)
				return
			}
			require.Equal(t, [2]int{tc.line, tc.column}, [2]int{line, column})
		})
	}
}
