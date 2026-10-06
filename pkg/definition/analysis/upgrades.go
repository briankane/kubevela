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
	"fmt"
	"regexp"
	"strings"

	"github.com/oam-dev/kubevela/pkg/cue/upgrade"
)

// Edit replaces lines [Start, End) of a text, counted from 0, with Text.
type Edit struct {
	Start, End int
	Text       string
}

// upgradeHunk is one place KubeVela's CUE upgrader changes a text: lines
// [start, end) of it become lines.
type upgradeHunk struct {
	start, end int
	lines      []string
}

var upgradeReason = regexp.MustCompile(`^\[[^\]]*\]\s*\[([^\]]+)\]\s*(.*)$`)

// upgradeHunks are the changes KubeVela's CUE upgrader makes to src, line by
// line, setting aside the whitespace its formatting changes, and why it makes
// them, by upgrade ID.
func upgradeHunks(src string) ([]upgradeHunk, map[string]string) {
	need, reasons, err := upgrade.RequiresUpgrade(src)
	if err != nil || !need {
		return nil, nil
	}
	out, err := upgrade.Upgrade(src)
	if err != nil {
		return nil, nil
	}
	why := map[string]string{}
	for _, r := range reasons {
		if m := upgradeReason.FindStringSubmatch(r); m != nil {
			why[m[1]] = m[2]
		}
	}
	a := strings.Split(src, "\n")
	var hunks []upgradeHunk
	for _, h := range diffLines(a, strings.Split(out, "\n")) {
		// The upgrader's formatting adds and drops blank lines, which change
		// nothing.
		if !blank(a[h.start:h.end]) || !blank(h.lines) {
			hunks = append(hunks, h)
		}
	}
	return hunks, why
}

func blank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// diffLines are the hunks turning a into b, comparing lines without their
// whitespace, which formatting changes and tokens never need.
func diffLines(a, b []string) []upgradeHunk {
	norm := func(s string) string { return strings.Join(strings.Fields(s), "") }
	n, m := len(a), len(b)
	// lcs[i][j] is the longest common subsequence of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if norm(a[i]) == norm(b[j]) {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var hunks []upgradeHunk
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && norm(a[i]) == norm(b[j]) {
			i, j = i+1, j+1
			continue
		}
		h := upgradeHunk{start: i}
		for (i < n || j < m) && !(i < n && j < m && norm(a[i]) == norm(b[j])) {
			if j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]) {
				h.lines = append(h.lines, b[j])
				j++
			} else {
				i++
			}
		}
		h.end = i
		hunks = append(hunks, h)
	}
	return hunks
}

// upgradeID is the upgrade a hunk comes from, read from what it writes.
func upgradeID(h upgradeHunk, why map[string]string) string {
	text := strings.Join(h.lines, "\n")
	switch {
	case strings.Contains(text, "list.Concat") || strings.Contains(text, "list.Repeat"):
		if _, ok := why["list-arithmetic"]; ok {
			return "list-arithmetic"
		}
	case strings.Contains(text, `"error"`):
		if _, ok := why["error-field-label"]; ok {
			return "error-field-label"
		}
	}
	var rest []string
	for id := range why {
		if id != "list-arithmetic" && id != "error-field-label" {
			rest = append(rest, id)
		}
	}
	if len(rest) == 1 {
		return rest[0]
	}
	return strings.Join(rest, ", ")
}

// checkUpgrades warns where KubeVela's CUE upgrader rewrites the file when it
// loads the definition, with the rewrite to make.
func (d *document) checkUpgrades() []Diagnostic {
	hunks, why := upgradeHunks(string(d.src))
	lines := strings.Split(string(d.src), "\n")
	var diags []Diagnostic
	for _, h := range hunks {
		if h.end == h.start {
			// What the rewrite adds, such as the import list.Concat needs,
			// goes with the change that needs it.
			continue
		}
		id := upgradeID(h, why)
		var rewrite []string
		for _, l := range h.lines {
			rewrite = append(rewrite, strings.TrimSpace(l))
		}
		reason := why[id]
		msg := fmt.Sprintf("KubeVela's CUE upgrader rewrites this when it loads the definition (%s: %s): rewrite it as %s", id, reason, strings.Join(rewrite, " "))
		end := Position{Line: h.end, Column: len(lines[h.end-1]) + 1}
		diags = append(diags, Diagnostic{
			Range:    Range{Start: Position{Line: h.start + 1, Column: 1 + len(lines[h.start]) - len(strings.TrimLeft(lines[h.start], " \t"))}, End: end},
			Severity: SeverityWarning,
			Message:  msg,
		})
	}
	return diags
}

// UpgradeEdits are the edits that make KubeVela's CUE upgrades to src, and
// only those: lines the upgrader merely reformats are left as they are.
func UpgradeEdits(src string) []Edit {
	hunks, _ := upgradeHunks(src)
	var edits []Edit
	for _, h := range hunks {
		text := ""
		if len(h.lines) > 0 {
			text = strings.Join(h.lines, "\n") + "\n"
		}
		edits = append(edits, Edit{Start: h.start, End: h.end, Text: text})
	}
	return edits
}

// ApplyEdits applies edits to src, as a client applies them.
func ApplyEdits(src string, edits []Edit) string {
	lines := strings.SplitAfter(src, "\n")
	var b strings.Builder
	at := 0
	for _, e := range edits {
		for ; at < e.Start && at < len(lines); at++ {
			b.WriteString(lines[at])
		}
		b.WriteString(e.Text)
		at = e.End
	}
	for ; at < len(lines); at++ {
		b.WriteString(lines[at])
	}
	return b.String()
}

// supersededByUpgrade is CUE's error for list arithmetic, which KubeVela's
// upgrader rewrites before the controller compiles a definition.
var supersededByUpgrade = regexp.MustCompile(`of lists is superseded by list\.`)

// withoutUpgraded drops CUE's errors for what the upgrader rewrites, where
// the upgrade's own warning says so on the same line.
func withoutUpgraded(diags []Diagnostic) []Diagnostic {
	upgraded := map[int]bool{}
	for _, d := range diags {
		if strings.Contains(d.Message, "CUE upgrader") {
			for l := d.Range.Start.Line; l <= d.Range.End.Line; l++ {
				upgraded[l] = true
			}
		}
	}
	out := diags[:0]
	for _, d := range diags {
		if supersededByUpgrade.MatchString(d.Message) && upgraded[d.Range.Start.Line] {
			continue
		}
		out = append(out, d)
	}
	return out
}
