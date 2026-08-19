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
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	velacommon "github.com/oam-dev/kubevela/pkg/utils/common"
	cmdutil "github.com/oam-dev/kubevela/pkg/utils/util"
)

func TestFormatAutoUpdate(t *testing.T) {
	r := require.New(t)
	yes, no := true, false
	// Nil and false must not render alike: an Application reconciled before the
	// field existed reports neither, and showing that as "false" would assert
	// something the controller never said.
	r.Equal("-", formatAutoUpdate(nil))
	r.Equal("true", formatAutoUpdate(&yes))
	r.Equal("false", formatAutoUpdate(&no))
}

func TestFormatValueUnquotesScalarsButNotCollections(t *testing.T) {
	r := require.New(t)
	raw := func(s string) *runtime.RawExtension { return &runtime.RawExtension{Raw: []byte(s)} }

	// A quoted string in a table column is noise; a collection keeps its braces
	// so it is obvious the whole thing substituted.
	r.Equal("nginx:1.27", formatValue(raw(`"nginx:1.27"`)))
	r.Equal("[8080,8443]", formatValue(raw(`[8080,8443]`)))
	r.Equal(`{"team":"platform"}`, formatValue(raw(`{"team":"platform"}`)))
	r.Equal("5432", formatValue(raw(`5432`)))
	r.Equal("-", formatValue(nil))
}

func TestFormatReaderAndPlacement(t *testing.T) {
	r := require.New(t)
	r.Equal("component/web (webservice)", formatReader(common.SourceConsumer{
		DefinitionKind: "component", Name: "web", Type: "webservice"}))
	// A workflow step has no placement, so it must not render a stray separator.
	r.Equal("workflowstep/notify", formatReader(common.SourceConsumer{
		DefinitionKind: "workflowstep", Name: "notify"}))
	// A placed reader always carries a cluster name, so an empty one means the
	// reader is not placed - a workflow step - rather than running locally.
	r.Equal("local", formatCluster("local"))
	r.Equal("eu-west", formatCluster("eu-west"))
	r.Equal("-", formatCluster(""))
}

func TestConsumerFilterComposesWithTheExistingFlags(t *testing.T) {
	r := require.New(t)
	web := common.SourceConsumer{Name: "web", Cluster: "local"}
	db := common.SourceConsumer{Name: "db", Cluster: "remote"}

	r.True(Filter{}.matchConsumer(web), "no filter matches everything")
	r.True(Filter{Component: "web"}.matchConsumer(web))
	r.False(Filter{Component: "web"}.matchConsumer(db))
	r.True(Filter{Cluster: "remote"}.matchConsumer(db))
	r.False(Filter{Cluster: "remote"}.matchConsumer(web))
}

func sourcesFixture() *v1beta1.Application {
	yes := true
	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "prod"},
		Spec:       v1beta1.ApplicationSpec{Sources: []v1beta1.ApplicationSource{{Name: "registry"}}},
		Status: common.AppStatus{Sources: []common.ApplicationSourceStatus{{
			Name: "registry", Type: "configmap@v2", Phase: "Resolved", AutoUpdate: &yes,
			Resolutions: []common.SourceResolution{{StorageKey: "cm-local-a1", Clusters: []string{"local"}, Phase: "Resolved"}},
			ConsumedBy: []common.SourceConsumer{
				{DefinitionKind: "component", Name: "web", Cluster: "local", Namespace: "prod",
					Values: []common.SourceValue{{Property: "image", SourceAttr: "data.image",
						Value: &runtime.RawExtension{Raw: []byte(`"nginx:1.27"`)}}}},
				{DefinitionKind: "component", Name: "api", Cluster: "eu-west", Namespace: "prod",
					Values: []common.SourceValue{{Property: "image", SourceAttr: "data.image",
						Value: &runtime.RawExtension{Raw: []byte(`"nginx:1.27"`)}}}},
			},
		}}},
	}
}

// The table is for reading; -o is for scripting. Without it the only way to get
// at this is scraping column output, which is exactly what a stable format
// exists to avoid.
func TestPrintAppSourcesMachineReadable(t *testing.T) {
	r := require.New(t)
	cli := fake.NewClientBuilder().WithScheme(velacommon.Scheme).WithObjects(sourcesFixture()).Build()

	var buf bytes.Buffer
	r.NoError(printAppSources(context.Background(), cli, "prod", "checkout", Filter{}, "json", &buf))
	var got sourcesOutput
	r.NoError(json.Unmarshal(buf.Bytes(), &got))
	r.Equal("checkout", got.Name)
	r.Equal("prod", got.Namespace)
	r.Len(got.Sources, 1)
	r.Equal("registry", got.Sources[0].Name)
	r.Len(got.Sources[0].ConsumedBy, 2)

	buf.Reset()
	r.NoError(printAppSources(context.Background(), cli, "prod", "checkout", Filter{}, "yaml", &buf))
	r.Contains(buf.String(), "sourceAttr: data.image")
	r.NotContains(buf.String(), "+---", "yaml output must not carry table decoration")
}

// A filter that narrowed the table but not the machine-readable form would be a
// trap for anything scripting against it.
func TestPrintAppSourcesFiltersMachineReadableToo(t *testing.T) {
	r := require.New(t)
	cli := fake.NewClientBuilder().WithScheme(velacommon.Scheme).WithObjects(sourcesFixture()).Build()

	var buf bytes.Buffer
	r.NoError(printAppSources(context.Background(), cli, "prod", "checkout",
		Filter{Cluster: "eu-west"}, "json", &buf))
	var got sourcesOutput
	r.NoError(json.Unmarshal(buf.Bytes(), &got))
	r.Len(got.Sources, 1, "the binding is still reported: whether it resolved does not depend on the filter")
	r.Len(got.Sources[0].ConsumedBy, 1)
	r.Equal("api", got.Sources[0].ConsumedBy[0].Name)
}

func TestPrintAppSourcesJSONPath(t *testing.T) {
	r := require.New(t)
	cli := fake.NewClientBuilder().WithScheme(velacommon.Scheme).WithObjects(sourcesFixture()).Build()
	var buf bytes.Buffer
	r.NoError(printAppSources(context.Background(), cli, "prod", "checkout", Filter{},
		"jsonpath={.sources[0].phase}", &buf))
	r.Equal("Resolved", buf.String())
}

func TestSourceIndicatorVocabulary(t *testing.T) {
	r := require.New(t)
	r.Equal(emojiSucceed, sourceIndicator("Resolved"))
	r.Equal(emojiFail, sourceIndicator("Failed"))
	r.Equal(emojiSkip, sourceIndicator("Unused"))
	// Stale is not a failure. The Application works; its data has stopped moving,
	// and a cross would say something untrue.
	r.Equal(emojiExecuting, sourceIndicator("Stale"))
	// An unknown phase from a newer controller reads as in-progress rather than
	// as success, which is the safe direction.
	r.Equal(emojiExecuting, sourceIndicator("SomethingNew"))
	r.Equal(emojiExecuting, sourceIndicator(""))
}

func TestPrintSourcesOverview(t *testing.T) {
	r := require.New(t)
	yes := true
	app := &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "prod"},
		Spec: v1beta1.ApplicationSpec{Sources: []v1beta1.ApplicationSource{
			{Name: "registry", Type: "configmap"}, {Name: "pending", Type: "atlas"},
		}},
		Status: common.AppStatus{Sources: []common.ApplicationSourceStatus{
			{Name: "registry", Type: "configmap", Phase: "Failed", AutoUpdate: &yes,
				Message: "vault: permission denied"},
		}},
	}
	var buf bytes.Buffer
	printSourcesOverview(cmdutil.IOStreams{Out: &buf, ErrOut: &buf}, app)
	out := buf.String()

	r.Contains(out, "registry (configmap)")
	r.Contains(out, emojiFail)
	// Name, type and indicator only. The reason, the cache entry and who consumed
	// what are --sources' job; this block sits beside Services, not above it.
	r.NotContains(out, "vault: permission denied")
	r.NotContains(out, "Resolved")
	// Declared but not yet in status still appears, as in-progress: silence would
	// read as "no such source" rather than "not resolved yet".
	r.Contains(out, "pending (atlas)")
	r.Contains(out, emojiExecuting)

	// An Application with no sources says nothing at all.
	buf.Reset()
	printSourcesOverview(cmdutil.IOStreams{Out: &buf, ErrOut: &buf},
		&v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: "b"}})
	r.Empty(buf.String())
}

// A binding fanned across clusters has an entry per cluster, each with its own
// expiry and its own state. Collapsing them named one arbitrarily.
func TestDividedResolutionsOnlyWhenTheyDiffer(t *testing.T) {
	r := require.New(t)
	same := common.ApplicationSourceStatus{Resolutions: []common.SourceResolution{
		{StorageKey: "a", Clusters: []string{"eu-west"}, Phase: "Resolved"},
		{StorageKey: "b", Clusters: []string{"us-east"}, Phase: "Resolved"},
	}}
	r.Nil(dividedResolutions(same), "resolving the same way everywhere stays one line")

	split := common.ApplicationSourceStatus{Resolutions: []common.SourceResolution{
		{StorageKey: "a", Clusters: []string{"eu-west"}, Phase: "Resolved"},
		{StorageKey: "b", Clusters: []string{"us-east"}, Phase: "Failed"},
	}}
	r.Len(dividedResolutions(split), 2, "one cluster failing must be distinguishable from all of them failing")

	single := common.ApplicationSourceStatus{Resolutions: []common.SourceResolution{
		{StorageKey: "a", Clusters: []string{"local"}, Phase: "Failed"},
	}}
	r.Nil(dividedResolutions(single), "a single-cluster app gains nothing from a breakdown")
}

func TestPrintSourcesOverviewSplitsDivergentClusters(t *testing.T) {
	r := require.New(t)
	app := &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "prod"},
		Spec: v1beta1.ApplicationSpec{Sources: []v1beta1.ApplicationSource{
			{Name: "registry", Type: "configmap"}, {Name: "steady", Type: "configmap"},
		}},
		Status: common.AppStatus{Sources: []common.ApplicationSourceStatus{
			{Name: "registry", Type: "configmap", Phase: "Failed", Resolutions: []common.SourceResolution{
				{StorageKey: "a", Clusters: []string{"eu-west"}, Phase: "Resolved"},
				{StorageKey: "b", Clusters: []string{"us-east"}, Phase: "Failed"},
			}},
			{Name: "steady", Type: "configmap", Phase: "Resolved", Resolutions: []common.SourceResolution{
				{StorageKey: "c", Clusters: []string{"eu-west"}, Phase: "Resolved"},
				{StorageKey: "d", Clusters: []string{"us-east"}, Phase: "Resolved"},
			}},
		}},
	}
	var buf bytes.Buffer
	printSourcesOverview(cmdutil.IOStreams{Out: &buf, ErrOut: &buf}, app)
	out := buf.String()
	r.Contains(out, "eu-west")
	r.Contains(out, "us-east")
	// The binding that behaved the same everywhere stays a single line.
	r.Equal(1, strings.Count(out, "steady"))
}

func consumer(kind, name, cluster string) common.SourceConsumer {
	return common.SourceConsumer{DefinitionKind: kind, Name: name, Cluster: cluster}
}

// One component placed in three clusters is one reader that runs in three
// places. Listing it three times would say more about the topology than about
// the source.
func TestSummariseReadersDeduplicatesPlacements(t *testing.T) {
	r := require.New(t)
	src := common.ApplicationSourceStatus{ConsumedBy: []common.SourceConsumer{
		consumer("component", "web", "eu-west"),
		consumer("component", "web", "us-east"),
		consumer("trait", "web/ingress", "eu-west"),
		consumer("workflowstep", "notify", ""),
	}}
	r.Equal("component/web, trait/web/ingress, workflowstep/notify", summariseReaders(src))
}

// A binding read by thirty components is worth knowing; thirty names wrapped
// across a terminal is not.
func TestSummariseReadersTruncates(t *testing.T) {
	r := require.New(t)
	var src common.ApplicationSourceStatus
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		src.ConsumedBy = append(src.ConsumedBy, consumer("component", n, "local"))
	}
	got := summariseReaders(src)
	r.Contains(got, "and 2 more")
	r.Contains(got, "component/a")
	r.NotContains(got, "component/f")
}

func TestSummariseReadersSilentWhenNothingRead(t *testing.T) {
	require.Equal(t, "", summariseReaders(common.ApplicationSourceStatus{}))
}
