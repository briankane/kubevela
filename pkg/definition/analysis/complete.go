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
	"regexp"
	"sort"
	"strings"
)

// Completion is one candidate to complete the text before the cursor with.
type Completion struct {
	// Label is what the candidate is shown as.
	Label string
	// Insert replaces the last Replace bytes before the cursor.
	Insert  string
	Replace int
	Doc     string
}

var (
	markerNameTyped  = regexp.MustCompile(`^\s*//\s*\+((?:[A-Za-z][A-Za-z0-9]*(?::[A-Za-z0-9]*)?)?)$`)
	markerValueTyped = regexp.MustCompile(`^\s*//\s*\+([A-Za-z][A-Za-z0-9]*(?::[A-Za-z][A-Za-z0-9]*)?)=(\S*)$`)
)

// CompleteMarker completes a marker in a comment, given its line up to the
// cursor: a marker's name after `// +`, or one of the values it takes after
// `=`.
func CompleteMarker(before string) []Completion {
	if m := markerNameTyped.FindStringSubmatch(before); m != nil {
		typed := m[1]
		var out []Completion
		for _, mk := range Markers() {
			if !strings.HasPrefix(strings.ToLower(mk.Name), strings.ToLower(typed)) {
				continue
			}
			insert := mk.Name
			if !mk.Bare {
				insert += "="
			}
			out = append(out, Completion{Label: "+" + mk.Name, Insert: insert, Replace: len(typed), Doc: mk.Doc})
		}
		return out
	}
	if m := markerValueTyped.FindStringSubmatch(before); m != nil {
		mk := findMarker(Markers(), m[1])
		if mk == nil {
			return nil
		}
		values := append([]string{}, mk.Values...)
		sort.Strings(values)
		var out []Completion
		for _, v := range values {
			if strings.HasPrefix(v, m[2]) {
				out = append(out, Completion{Label: v, Insert: v, Replace: len(m[2]), Doc: mk.Doc})
			}
		}
		return out
	}
	return nil
}
