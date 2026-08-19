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
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
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
