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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
)

// printAppSources renders what an Application read from its declared sources.
//
// Two tables rather than one. The first answers "did my data arrive", which is a
// property of the binding and is the same whichever component you look at. The
// second answers "who used it and what did they get", which is genuinely per
// reader and per placement, and is the part that is unreadable as raw YAML once
// there is more than one cluster.
func printAppSources(ctx context.Context, cli client.Client, namespace, appName string, filter Filter) error {
	app, err := loadRemoteApplication(cli, namespace, appName)
	if err != nil {
		return err
	}
	if len(app.Spec.Sources) == 0 {
		fmt.Printf("Application %s/%s declares no sources.\n", namespace, appName)
		return nil
	}
	if len(app.Status.Sources) == 0 {
		fmt.Printf("Application %s/%s has declared sources but has not resolved them yet.\n", namespace, appName)
		return nil
	}

	fmt.Printf("Sources of %s/%s:\n\n", namespace, appName)
	summary := tablewriter.NewWriter(os.Stdout)
	summary.SetColWidth(60)
	summary.SetHeader([]string{"NAME", "TYPE", "PHASE", "AUTO-UPDATE", "EXPIRES", "CACHE ENTRY"})
	for _, src := range app.Status.Sources {
		summary.Append([]string{
			src.Name,
			orDash(src.Type),
			orDash(src.Phase),
			formatAutoUpdate(src.AutoUpdate),
			orDash(src.ExpiresAt),
			orDash(src.Config),
		})
	}
	summary.Render()

	// A message is the only place a false auto-update says which of the gate, the
	// binding and a publishVersion pin won, so it must not be swallowed by the
	// table's column width.
	for _, src := range app.Status.Sources {
		if src.Message != "" {
			fmt.Printf("\n%s: %s\n", src.Name, src.Message)
		}
	}

	fmt.Printf("\nConsumed by:\n\n")
	reads := tablewriter.NewWriter(os.Stdout)
	reads.SetColWidth(60)
	reads.SetHeader([]string{"SOURCE", "READER", "PLACEMENT", "PROPERTY", "SOURCE ATTR", "VALUE"})
	rows := 0
	for _, src := range app.Status.Sources {
		for _, by := range src.ConsumedBy {
			if !filter.matchConsumer(by) {
				continue
			}
			for _, v := range by.Values {
				reads.Append([]string{
					src.Name,
					formatReader(by),
					orDash(formatPlacement(by)),
					orDash(v.Property),
					v.SourceAttr,
					formatValue(v.Value),
				})
				rows++
			}
		}
	}
	if rows == 0 {
		fmt.Println("  (nothing has consumed a source value)")
		return nil
	}
	reads.Render()
	return nil
}

// matchConsumer applies the component and cluster filters the other status views
// already accept, so --sources composes with them rather than inventing its own.
func (f Filter) matchConsumer(by common.SourceConsumer) bool {
	if f.Component != "" && by.Name != f.Component {
		return false
	}
	if f.Cluster != "" && by.Cluster != f.Cluster {
		return false
	}
	return true
}

func formatReader(by common.SourceConsumer) string {
	out := by.DefinitionKind + "/" + by.Name
	if by.Type != "" {
		out += " (" + by.Type + ")"
	}
	return out
}

func formatPlacement(by common.SourceConsumer) string {
	parts := make([]string, 0, 2)
	if by.Cluster != "" {
		parts = append(parts, by.Cluster)
	}
	if by.Namespace != "" {
		parts = append(parts, by.Namespace)
	}
	return strings.Join(parts, "/")
}

// formatAutoUpdate distinguishes "off" from "not reported", which a bare bool
// cannot: an Application reconciled before this field existed has neither.
func formatAutoUpdate(b *bool) string {
	if b == nil {
		return "-"
	}
	if *b {
		return "true"
	}
	return "false"
}

func formatValue(raw interface{}) string {
	if raw == nil {
		return "-"
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "-"
	}
	out := string(b)
	// A scalar string reads better without the JSON quoting; a collection keeps
	// its braces so it is obvious it is one.
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return out
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

