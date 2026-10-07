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
	"strings"
)

var (
	// nameTyped is a name begun at the end of the text before the cursor,
	// after what precedes a reference: an operator, a bracket or a colon.
	nameTyped = regexp.MustCompile(`(?:^|[:&|(,\[+\-*/=!<>]\s*)([A-Za-z_#$][A-Za-z0-9_#$]*)$`)
	// importBlock is an import declaration, single or a block; importLine one
	// of its specs, with its name if any.
	importBlock = regexp.MustCompile(`(?m)^import\s*(\([^)]*\)|"[^"]*"|[A-Za-z_][A-Za-z0-9_]*\s+"[^"]*")`)
	importLine  = regexp.MustCompile(`(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?"([^"]+)"`)
	// declaredName is a helper, a definition or a let the file declares.
	declaredName = regexp.MustCompile(`(?m)^\s*(?:(_[A-Za-z0-9_]*|#[A-Za-z0-9_]+)\??:|let\s+([A-Za-z_][A-Za-z0-9_]*)\s*=)`)
)

// CompleteRootName completes a reference's first name where one is begun at
// cursor in doc: context, parameter, the file's imports by their names, and
// the helpers, definitions and lets it declares. It reads the text, which
// need not parse while the name is typed, and offers nothing where a field's
// label is written, after a dot, or before a name is begun.
func CompleteRootName(doc string, cursor int) []Completion {
	lineStart := strings.LastIndex(doc[:cursor], "\n") + 1
	line := doc[lineStart:cursor]
	m := nameTyped.FindStringSubmatchIndex(line)
	if m == nil || m[0] == 0 && strings.TrimSpace(line) == line[m[2]:m[3]] {
		return nil
	}
	typed := line[m[2]:m[3]]
	var out []Completion
	add := func(name, detail, doc string) {
		if strings.HasPrefix(name, typed) && name != typed {
			for _, c := range out {
				if c.Label == name {
					return
				}
			}
			out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: detail, Doc: doc})
		}
	}
	add(contextLabel, "context", "What KubeVela gives the template at render: the application's and component's names, namespace, cluster and more.")
	add(parameterLabel, "parameter", "The parameters the template declares, as the user gives them.")
	for _, block := range importBlock.FindAllStringSubmatch(doc, -1) {
		for _, spec := range importLine.FindAllStringSubmatch(block[1], -1) {
			name := spec[1]
			if name == "" {
				name = spec[2][strings.LastIndex(spec[2], "/")+1:]
			}
			add(name, "import", "The "+spec[2]+" package.")
		}
	}
	for _, d := range declaredName.FindAllStringSubmatch(doc, -1) {
		switch {
		case d[1] != "":
			add(d[1], "declared here", "")
		case d[2] != "":
			add(d[2], "let", "")
		}
	}
	return out
}
