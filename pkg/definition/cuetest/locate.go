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
	"strings"

	"cuelang.org/go/cue"
)

// Locate is where in the test file a failure of this case points: the
// expected field its path names, or the deepest expected field on that path
// when the failure is about a field the expectation does not have. A failure
// that names no expected field is at the case's line, column 0.
func (c *Case) Locate(failure string) (line, column int) {
	for i := strings.Index(failure, ": "); i >= 0; {
		if p := cue.ParsePath(failure[:i]); p.Err() == nil {
			sels := p.Selectors()
			for n := len(sels); n > 0; n-- {
				v := c.Expect.LookupPath(cue.MakePath(sels[:n]...))
				if pos := v.Pos(); v.Exists() && pos.IsValid() && c.inTestFile(pos.Filename()) {
					return pos.Line(), pos.Column()
				}
			}
		}
		next := strings.Index(failure[i+2:], ": ")
		if next < 0 {
			break
		}
		i += 2 + next
	}
	return c.Line, 0
}

// inTestFile reports whether a position's file is the case's test file,
// which is compiled from its text under the name "-".
func (c *Case) inTestFile(name string) bool {
	return name == "-" || name == c.File
}
