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
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	cmdutil "github.com/oam-dev/kubevela/pkg/utils/util"
)

// printAppSources renders what an Application read from its declared sources.
//
// Two tables rather than one. The first answers "did my data arrive", which is a
// property of the binding and is the same whichever component you look at. The
// second answers "who used it and what did they get", which is genuinely per
// reader and per placement, and is the part that is unreadable as raw YAML once
// there is more than one cluster.
func printAppSources(ctx context.Context, cli client.Client, namespace, appName string,
	filter Filter, outputFormat string, out io.Writer) error {
	app, err := loadRemoteApplication(cli, namespace, appName)
	if err != nil {
		return err
	}
	sources := filterSources(app.Status.Sources, filter)

	// A machine-readable form of the same thing, so this is scriptable rather
	// than only readable. The filters apply here too - narrowing to a cluster and
	// then getting every cluster back would be a surprise.
	//
	// Wrapped in an object rather than emitted as a bare array because jsonpath
	// cannot address one: RelaxedJSONPathExpression turns every form of
	// "{.[0].phase}" into an empty result, silently. The wrapper also gives the
	// output somewhere to say which Application it describes.
	if outputFormat != "" {
		str, err := printObj(outputFormat, sourcesOutput{
			Name:      appName,
			Namespace: namespace,
			Sources:   sources,
		})
		if err != nil {
			return err
		}
		_, err = out.Write([]byte(str))
		return err
	}

	if len(app.Spec.Sources) == 0 {
		fmt.Fprintf(out, "Application %s/%s declares no sources.\n", namespace, appName)
		return nil
	}
	if len(app.Status.Sources) == 0 {
		fmt.Fprintf(out, "Application %s/%s has declared sources but has not resolved them yet.\n", namespace, appName)
		return nil
	}

	fmt.Fprintf(out, "Sources of %s/%s:\n\n", namespace, appName)
	summary := tablewriter.NewWriter(out)
	summary.SetColWidth(60)
	summary.SetHeader([]string{"NAME", "TYPE", "PHASE", "AUTO-UPDATE", "EXPIRES", "CACHE ENTRY"})
	for _, src := range sources {
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
	for _, src := range sources {
		if src.Message != "" {
			fmt.Fprintf(out, "\n%s: %s\n", src.Name, src.Message)
		}
	}

	fmt.Fprintf(out, "\nConsumed by:\n\n")
	reads := tablewriter.NewWriter(out)
	reads.SetColWidth(60)
	reads.SetHeader([]string{"SOURCE", "READER", "CLUSTER", "NAMESPACE", "PROPERTY", "SOURCE ATTR", "VALUE"})
	rows := 0
	for _, src := range sources {
		for _, by := range src.ConsumedBy {
			for _, v := range by.Values {
				reads.Append([]string{
					src.Name,
					formatReader(by),
					formatCluster(by.Cluster),
					orDash(by.Namespace),
					orDash(v.Property),
					v.SourceAttr,
					formatValue(v.Value),
				})
				rows++
			}
		}
	}
	if rows == 0 {
		fmt.Fprintln(out, "  (nothing has consumed a source value)")
		return nil
	}
	reads.Render()
	return nil
}

// sourcesOutput is the machine-readable shape of this view.
type sourcesOutput struct {
	Name      string                           `json:"name"`
	Namespace string                           `json:"namespace"`
	Sources   []common.ApplicationSourceStatus `json:"sources"`
}

// filterSources narrows who is reported without dropping any binding. The
// summary answers "did my data arrive", which is a property of the binding and
// is true regardless of which cluster you asked about - hiding a stale source
// because you filtered to one cluster would be worse than useless.
func filterSources(sources []common.ApplicationSourceStatus, filter Filter) []common.ApplicationSourceStatus {
	out := make([]common.ApplicationSourceStatus, 0, len(sources))
	for _, src := range sources {
		kept := make([]common.SourceConsumer, 0, len(src.ConsumedBy))
		for _, by := range src.ConsumedBy {
			if filter.matchConsumer(by) {
				kept = append(kept, by)
			}
		}
		src.ConsumedBy = kept
		out = append(out, src)
	}
	return out
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

// formatCluster renders the recorded cluster. A placed reader always has one -
// the controller names the local cluster rather than leaving it blank - so an
// empty value here means the reader is not placed at all, which is true of a
// workflow step and is not the same as running locally.
func formatCluster(cluster string) string {
	return orDash(cluster)
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

// sourceIndicator maps a phase to the vocabulary the rest of vela status already
// uses, so a source reads the same way a component or a workflow step does.
//
// Stale is deliberately not a failure. A stale source is serving its previous
// value, which means the Application is working and its data has stopped moving
// - worth attention, but a cross would say something untrue. Unused is not a
// problem at all, only a fact.
func sourceIndicator(phase string) string {
	switch strings.ToLower(phase) {
	case "resolved":
		return emojiSucceed
	case "failed":
		return emojiFail
	case "unused":
		return emojiSkip
	default: // stale, pending, anything a newer controller reports
		return emojiExecuting
	}
}

// printSourcesOverview lists each declared binding and how it is doing, in the
// default status view.
//
// It sits with the Application's own summary rather than under Services because
// a binding is declared once for the whole Application: the same source feeds
// several components, and reporting it per component is what the status rework
// moved away from.
func printSourcesOverview(ioStreams cmdutil.IOStreams, app *v1beta1.Application) {
	if len(app.Spec.Sources) == 0 {
		return
	}
	ioStreams.Infof("Sources:\n\n")
	byName := map[string]common.ApplicationSourceStatus{}
	for _, src := range app.Status.Sources {
		byName[src.Name] = src
	}
	for _, declared := range app.Spec.Sources {
		src, resolved := byName[declared.Name]
		if !resolved {
			ioStreams.Infof("  - %s %s (%s)  not resolved yet\n",
				emojiExecuting, declared.Name, orDash(declared.Type))
			continue
		}
		line := fmt.Sprintf("  - %s %s (%s)  %s", sourceIndicator(src.Phase),
			src.Name, orDash(src.Type), orDash(src.Phase))
		if src.AutoUpdate != nil && *src.AutoUpdate {
			line += "  auto-update"
		}
		ioStreams.Infof("%s\n", line)
		if src.Message != "" {
			ioStreams.Infof("      %s\n", src.Message)
		}
	}
	ioStreams.Infof("\n")
}
