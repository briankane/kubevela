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
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/token"
)

// JoinedClusters are the clusters joined to the cluster the kubeconfig
// names, local among them.
type JoinedClusters struct {
	Context  string
	Clusters []JoinedCluster
}

// JoinedCluster is a joined cluster's name and labels.
type JoinedCluster struct {
	Name   string
	Labels map[string]string
}

const topologyPolicy = "topology"

// The topology properties that select clusters by label; clusterSelector
// is the deprecated name of clusterLabelSelector.
var topologySelectors = []string{"clusterLabelSelector", "clusterSelector"}

func (j *JoinedClusters) names() []string {
	out := make([]string, 0, len(j.Clusters))
	for _, c := range j.Clusters {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// matches is whether any cluster has every label of selector.
func (j *JoinedClusters) matches(selector map[string]string) bool {
	for _, c := range j.Clusters {
		all := true
		for k, v := range selector {
			if c.Labels[k] != v {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// checkTopology warns of a topology policy naming a cluster not joined, or
// selecting by labels no joined cluster has.
func (d *document) checkTopology(app cue.Value, fields map[string]*ast.Field) []Diagnostic {
	j := d.opts.Clusters
	if j == nil {
		return nil
	}
	policies, err := app.LookupPath(cue.ParsePath("spec.policies")).List()
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, n := range j.names() {
		known[n] = true
	}
	var diags []Diagnostic
	for i := 0; policies.Next(); i++ {
		p := policies.Value()
		if typ, _ := p.LookupPath(cue.ParsePath("type")).String(); typ != topologyPolicy {
			continue
		}
		props := p.LookupPath(cue.ParsePath("properties"))
		if list, err := props.LookupPath(cue.ParsePath("clusters")).List(); err == nil {
			for list.Next() {
				name, err := list.Value().String()
				if err != nil || strings.Contains(name, "$(") || known[name] {
					continue
				}
				r := d.itemRange(list.Value().Pos(), name)
				diag := Diagnostic{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("no cluster named %s is joined to %s", name, j.Context)}
				if to := closest(name, j.names()); to != "" {
					diag.Fixes = []Fix{{Title: "Change to " + to, Edits: []RangeEdit{{Range: r, NewText: to}}}}
				}
				diags = append(diags, diag)
			}
		}
		for _, key := range topologySelectors {
			var selector map[string]string
			if props.LookupPath(cue.ParsePath(key)).Decode(&selector) != nil || len(selector) == 0 || j.matches(selector) {
				continue
			}
			r, ok := fieldRange(fields, fmt.Sprintf("spec.policies.%d.properties.%s", i, key))
			if !ok {
				continue
			}
			pairs := make([]string, 0, len(selector))
			for k, v := range selector {
				pairs = append(pairs, k+"="+v)
			}
			sort.Strings(pairs)
			diags = append(diags, Diagnostic{Range: r, Severity: SeverityWarning, Message: fmt.Sprintf("no cluster joined to %s has the labels %s", j.Context, strings.Join(pairs, ", "))})
		}
	}
	return diags
}

// itemRange is the range of a scalar list item's text, inside any quotes.
func (d *document) itemRange(pos token.Pos, text string) Range {
	line, col := pos.Line(), pos.Column()
	lines := strings.Split(string(d.src), "\n")
	if line >= 1 && line <= len(lines) && col >= 1 && col <= len(lines[line-1]) {
		if c := lines[line-1][col-1]; c == '"' || c == '\'' {
			col++
		}
	}
	return Range{Start: Position{Line: line, Column: col}, End: Position{Line: line, Column: col + len(text)}}
}

// completeTopology completes in a topology policy's properties: a joined
// cluster's name as an item of clusters, and a label key, or a key's
// values, under a cluster selector. It is false elsewhere.
func completeTopology(doc, above []string, last string, opts Options) ([]Completion, bool) {
	j := opts.Clusters
	if j == nil {
		return nil, false
	}
	m := yamlValueTyped.FindStringSubmatch(last)
	key, typed := "", ""
	var path []string
	var lines []int
	switch {
	case m != nil && m[2] == "":
		path, lines = yamlPathItems(above, len(m[1]), false)
		path, key, typed = append(path, m[3]), m[3], m[4]
		lines = append(lines, -1)
	default:
		k := yamlKeyTyped.FindStringSubmatch(last)
		if k == nil {
			return nil, false
		}
		indent := len(k[1])
		if k[2] != "" {
			indent += 2
		}
		path, lines = yamlPathItems(above, indent, k[2] != "")
		typed = k[3]
	}
	rest, ok := topologyPropertiesAt(doc, path, lines)
	if !ok {
		return nil, false
	}
	var candidates []string
	doc2 := ""
	switch {
	case len(rest) == 2 && rest[0] == "clusters" && rest[1] == listItem:
		candidates, doc2 = j.names(), "A cluster joined to "+j.Context
	case len(rest) == 1 && isSelector(rest[0]) && key == "":
		seen := map[string]bool{}
		for _, c := range j.Clusters {
			for k := range c.Labels {
				seen[k] = true
			}
		}
		for k := range seen {
			candidates = append(candidates, k)
		}
		doc2 = "A label of the clusters joined to " + j.Context
	case len(rest) == 2 && isSelector(rest[0]) && key != "":
		seen := map[string]bool{}
		for _, c := range j.Clusters {
			if v, ok := c.Labels[key]; ok {
				seen[v] = true
			}
		}
		for v := range seen {
			candidates = append(candidates, v)
		}
		doc2 = "A value of " + key + " on the clusters joined to " + j.Context
	default:
		return nil, false
	}
	sort.Strings(candidates)
	var out []Completion
	for _, c := range candidates {
		if strings.HasPrefix(c, typed) {
			out = append(out, Completion{Label: c, Insert: c, Replace: len(typed), Doc: doc2})
		}
	}
	return out, true
}

func isSelector(key string) bool {
	return key == topologySelectors[0] || key == topologySelectors[1]
}

// topologyPropertiesAt is the path under a topology policy's properties,
// when path is under one.
func topologyPropertiesAt(doc, path []string, lines []int) ([]string, bool) {
	for k := len(path) - 1; k > 2; k-- {
		if path[k] != "properties" || path[k-1] != listItem || path[k-2] != "policies" {
			continue
		}
		item, keyIndent := itemLines(doc, lines[k-1])
		if name, _ := itemType(item, keyIndent); name != topologyPolicy {
			return nil, false
		}
		return path[k+1:], true
	}
	return nil, false
}
