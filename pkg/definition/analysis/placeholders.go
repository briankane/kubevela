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
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"
)

// scaffoldPlaceholder marks a value a released vela def init leaves for the
// author to set, as "<change me> apps/v1".
const scaffoldPlaceholder = "<change me>"

// checkPlaceholders reports each string still marked as a scaffold's
// placeholder, with a fix that keeps the value it suggests.
func (d *document) checkPlaceholders() []Diagnostic {
	var diags []Diagnostic
	ast.Walk(d.file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.Contains(s, scaffoldPlaceholder) {
			return true
		}
		kept := strings.TrimSpace(strings.ReplaceAll(s, scaffoldPlaceholder, ""))
		r := span(lit.Pos(), lit.End())
		diags = append(diags, Diagnostic{
			Range:    r,
			Severity: SeverityError,
			Message:  "a placeholder from vela def init is left in: set the value and remove " + scaffoldPlaceholder,
			Fixes:    []Fix{{Title: "Use " + strconv.Quote(kept), Edits: []RangeEdit{{Range: r, NewText: strconv.Quote(kept)}}}},
		})
		return true
	}, nil)
	return diags
}
