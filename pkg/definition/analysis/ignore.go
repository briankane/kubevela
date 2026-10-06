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

	"cuelang.org/go/cue/ast"
)

// The ide: markers silence this analysis and nothing else: KubeVela's own
// readers of markers ignore keys they do not know.
const (
	ignoreMarker     = "ide:ignore"
	ignoreFileMarker = "ide:ignore-file"
)

var ignoreFileLine = regexp.MustCompile(`(?m)^\s*//\s*\+ide:ignore-file\s*$`)

// ignoresFile reports whether src carries +ide:ignore-file.
func ignoresFile(src []byte) bool {
	return ignoreFileLine.Match(src)
}

// lineRange is an inclusive range of lines.
type lineRange struct{ from, to int }

// ignoredLines are the lines of each field whose doc comment carries
// +ide:ignore, the field's whole value included.
func (d *document) ignoredLines() []lineRange {
	var out []lineRange
	ast.Walk(d.file, func(n ast.Node) bool {
		f, ok := n.(*ast.Field)
		if !ok {
			return true
		}
		for _, cg := range ast.Comments(f) {
			for _, c := range cg.List {
				if m := markerLine.FindStringSubmatch(strings.TrimSpace(c.Text)); m != nil && m[1] == ignoreMarker {
					out = append(out, lineRange{from: f.Pos().Line(), to: f.End().Line()})
					return false
				}
			}
		}
		return true
	}, nil)
	return out
}

// withoutIgnored drops the diagnostics that start on an ignored line.
func withoutIgnored(diags []Diagnostic, ignored []lineRange) []Diagnostic {
	if len(ignored) == 0 {
		return diags
	}
	out := diags[:0]
	for _, d := range diags {
		skip := false
		for _, r := range ignored {
			skip = skip || (d.Range.Start.Line >= r.from && d.Range.Start.Line <= r.to)
		}
		if !skip {
			out = append(out, d)
		}
	}
	return out
}
