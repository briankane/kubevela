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

func appWithSourcePhases(declared int, phases ...string) *v1beta1.Application {
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "prod"}}
	for i := 0; i < declared; i++ {
		app.Spec.Sources = append(app.Spec.Sources, v1beta1.ApplicationSource{Name: string(rune('a' + i))})
	}
	for i, p := range phases {
		app.Status.Sources = append(app.Status.Sources,
			common.ApplicationSourceStatus{Name: string(rune('a' + i)), Phase: p})
	}
	return app
}

// The default view must say nothing about sources to an Application that does
// not use them.
func TestSummariseSourcesIsSilentWithoutSources(t *testing.T) {
	require.Equal(t, "", summariseSources(appWithSourcePhases(0)))
}

// An Application can be Running and healthy while a source has quietly stopped
// refreshing, because a stale source serves its previous value rather than
// failing. That is the case this line exists for, so it has to stand out.
func TestSummariseSourcesFlagsTroubleFirst(t *testing.T) {
	r := require.New(t)

	clean := summariseSources(appWithSourcePhases(2, "Resolved", "Unused"))
	r.Contains(clean, "1 resolved, 1 unused")
	r.NotContains(clean, emojiFail, "nothing wrong, so no alarm")
	r.Contains(clean, "--sources")

	stale := summariseSources(appWithSourcePhases(3, "Resolved", "Stale", "Failed"))
	r.Contains(stale, emojiFail)
	// Worst first, and a fixed order so the line does not reshuffle per reconcile.
	r.True(strings.Index(stale, "1 failed") < strings.Index(stale, "1 stale"))
	r.True(strings.Index(stale, "1 stale") < strings.Index(stale, "1 resolved"))
}

func TestSummariseSourcesBeforeAnythingResolves(t *testing.T) {
	require.Equal(t, "2 declared, none resolved yet", summariseSources(appWithSourcePhases(2)))
}
