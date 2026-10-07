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

package lsp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/definition"
)

// A definition is read from the revision an Application rendered with, as it
// was then, not as the cluster holds it now.
func TestDefinitionFromRevision(t *testing.T) {
	then := &v1beta1.ComponentDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "boom", Namespace: "vela-system"},
		Spec:       v1beta1.ComponentDefinitionSpec{Schematic: &common.Schematic{CUE: &common.CUE{Template: "output: {apiVersion: \"v1\", kind: \"ConfigMap\"}\nparameter: count: string\n"}}},
	}
	rev := v1beta1.ApplicationRevision{}
	rev.Spec.ComponentDefinitions = map[string]*v1beta1.ComponentDefinition{"boom": then}

	obj, ok := definitionFromRevision(rev, "component", "boom")
	require.True(t, ok)
	assert.Equal(t, "ComponentDefinition", obj.GetKind(), "the snapshot is a definition, whatever its type meta")
	src, err := (&definition.Definition{Unstructured: *obj}).ToCUEString()
	require.NoError(t, err)
	assert.Contains(t, src, "count: string")

	_, ok = definitionFromRevision(rev, "trait", "boom")
	assert.False(t, ok, "a component is not a trait")
	_, ok = definitionFromRevision(rev, "component", "other")
	assert.False(t, ok)
}

// The revision is the one the workflow ran, else the latest.
func TestRevisionOf(t *testing.T) {
	app := unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{
		"latestRevision": map[string]interface{}{"name": "shop-v3"},
		"workflow":       map[string]interface{}{"appRevision": "shop-v2"},
	}}}
	assert.Equal(t, "shop-v2", revisionOf(app))
	unstructured.RemoveNestedField(app.Object, "status", "workflow")
	assert.Equal(t, "shop-v3", revisionOf(app))
}

// vela/clusterDefinition reads from an Application's revision when given one.
func TestClusterDefinitionFromRevision(t *testing.T) {
	cluster := func() (Cluster, error) {
		c, err := appliedWeb()
		c.RevisionDefinition = func(namespace, app, typ, name string) (*unstructured.Unstructured, string, error) {
			if namespace != "default" || app != "shop" || typ != "component" || name != "web" {
				return nil, "", errors.New("not recorded")
			}
			then := &v1beta1.ComponentDefinition{Spec: v1beta1.ComponentDefinitionSpec{Schematic: &common.Schematic{CUE: &common.CUE{Template: "output: {apiVersion: \"v1\", kind: \"Secret\"}\n"}}}}
			rev := v1beta1.ApplicationRevision{}
			rev.Spec.ComponentDefinitions = map[string]*v1beta1.ComponentDefinition{"web": then}
			obj, _ := definitionFromRevision(rev, typ, name)
			obj.SetName("web")
			return obj, "shop-v2", nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	m := c.response(c.send(MethodClusterDefinition, ClusterDefinitionParams{Type: "component", Name: "web", Application: "shop", Namespace: "default"}, true))
	var r ClusterDefinitionResult
	require.NoError(t, json.Unmarshal(m["result"], &r), string(m["error"]))
	assert.Equal(t, "shop-v2", r.Revision)
	assert.Contains(t, r.CUE, `kind: "Secret"`, "as the revision recorded it, not as applied now")

	m = c.response(c.send(MethodClusterDefinition, ClusterDefinitionParams{Type: "component", Name: "web", Application: "nope", Namespace: "default"}, true))
	assert.Contains(t, string(m["error"]), "not recorded")
}
