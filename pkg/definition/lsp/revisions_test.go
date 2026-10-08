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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

func revisionObject(name string, created metav1.Time, succeeded bool, publish string) v1beta1.ApplicationRevision {
	rev := v1beta1.ApplicationRevision{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", CreationTimestamp: created}}
	if publish != "" {
		rev.Annotations = map[string]string{"app.oam.dev/publishVersion": publish}
	}
	rev.Status.Succeeded = succeeded
	rev.Status.Workflow = &common.WorkflowStatus{Phase: "succeeded"}
	if !succeeded {
		rev.Status.Workflow.Phase = "failed"
	}
	return rev
}

// An Application's revisions are listed newest first, by the number each
// name ends in, the current one marked.
func TestRevisionInfos(t *testing.T) {
	at := metav1.Date(2026, 10, 8, 12, 0, 0, 0, metav1.Now().Location())
	revs := []v1beta1.ApplicationRevision{
		revisionObject("shop-v2", at, false, ""),
		revisionObject("shop-v10", at, true, "v1.2"),
		revisionObject("shop-v1", at, true, ""),
	}
	got := revisionInfos(revs, "shop-v10")
	require.Len(t, got, 3)
	assert.Equal(t, []string{"shop-v10", "shop-v2", "shop-v1"}, []string{got[0].Name, got[1].Name, got[2].Name})
	assert.Equal(t, RevisionInfo{Name: "shop-v10", Version: 10, Created: at.UTC().Format("2006-01-02T15:04:05Z"), Succeeded: true, Phase: "succeeded", PublishVersion: "v1.2", Current: true}, got[0])
	assert.False(t, got[1].Current)
	assert.Equal(t, "failed", got[1].Phase)
}

// A revision's Application is what was applied: its name, namespace, labels,
// annotations and spec, without what the cluster added.
func TestRevisionApplication(t *testing.T) {
	rev := v1beta1.ApplicationRevision{}
	rev.Spec.Application = v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shop", Namespace: "shop", UID: "u1", ResourceVersion: "9", Generation: 3,
			Labels:      map[string]string{"team": "a"},
			Annotations: map[string]string{"note": "x", "kubectl.kubernetes.io/last-applied-configuration": "{}"},
		},
		Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{Name: "web", Type: "webservice"}}},
	}
	out, err := revisionApplication(rev)
	require.NoError(t, err)
	var got map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(out), &got))
	assert.Equal(t, "core.oam.dev/v1beta1", got["apiVersion"])
	assert.Equal(t, "Application", got["kind"])
	assert.Equal(t, map[string]interface{}{"name": "shop", "namespace": "shop", "labels": map[string]interface{}{"team": "a"}, "annotations": map[string]interface{}{"note": "x"}}, got["metadata"])
	assert.Equal(t, "webservice", got["spec"].(map[string]interface{})["components"].([]interface{})[0].(map[string]interface{})["type"])
	assert.NotContains(t, got, "status")
}

// Rolling back gives the live Application the revision's spec, and keeps
// what the cluster holds of its metadata; one published by version gets a
// new version, which the controller waits for.
func TestRolledBack(t *testing.T) {
	live := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.oam.dev/v1beta1", "kind": "Application",
		"metadata": map[string]interface{}{"name": "shop", "namespace": "shop", "resourceVersion": "9"},
		"spec":     map[string]interface{}{"components": []interface{}{map[string]interface{}{"name": "web", "type": "worker"}}},
	}}
	rev := v1beta1.ApplicationRevision{ObjectMeta: metav1.ObjectMeta{Name: "shop-v2"}}
	rev.Spec.Application.Spec = v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{Name: "web", Type: "webservice"}}}

	out, err := rolledBack(live, rev)
	require.NoError(t, err)
	comps, _, _ := unstructured.NestedSlice(out.Object, "spec", "components")
	assert.Equal(t, "webservice", comps[0].(map[string]interface{})["type"])
	assert.Equal(t, "9", out.GetResourceVersion())
	assert.Empty(t, out.GetAnnotations()["app.oam.dev/publishVersion"])
	liveComps, _, _ := unstructured.NestedSlice(live.Object, "spec", "components")
	assert.Equal(t, "worker", liveComps[0].(map[string]interface{})["type"], "the live object is left as it was")

	live.SetAnnotations(map[string]string{"app.oam.dev/publishVersion": "v1.3"})
	out, err = rolledBack(live, rev)
	require.NoError(t, err)
	assert.Regexp(t, `^shop-v2-rollback-\d+$`, out.GetAnnotations()["app.oam.dev/publishVersion"])
}

// The revision requests answer from the cluster, and refuse without one.
func TestRevisionRequests(t *testing.T) {
	var rolled []string
	cluster := func() (Cluster, error) {
		c, err := velaCluster()
		c.Revisions = func(namespace, app string) ([]RevisionInfo, error) {
			return []RevisionInfo{{Name: app + "-v2", Version: 2, Current: true}, {Name: app + "-v1", Version: 1}}, nil
		}
		c.RevisionApplication = func(namespace, revision string) (string, error) {
			return "kind: Application\nmetadata:\n  name: " + revision + "\n", nil
		}
		c.Rollback = func(namespace, app, revision string) error {
			rolled = append(rolled, namespace+"/"+app+"@"+revision)
			return nil
		}
		return c, err
	}
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	c.drain()
	c.response(c.send("initialize", map[string]interface{}{}, true))
	c.send("initialized", map[string]interface{}{}, false)
	clusterStatus(t, c)

	var list RevisionsResult
	m := c.response(c.send(MethodRevisions, RevisionsParams{Namespace: "shop", Name: "shop"}, true))
	require.NoError(t, json.Unmarshal(m["result"], &list))
	assert.Equal(t, "k3d-test", list.Context)
	assert.Equal(t, []string{"shop-v2", "shop-v1"}, []string{list.Revisions[0].Name, list.Revisions[1].Name})

	var one RevisionResult
	m = c.response(c.send(MethodRevision, RevisionParams{Namespace: "shop", Revision: "shop-v1"}, true))
	require.NoError(t, json.Unmarshal(m["result"], &one))
	assert.Contains(t, one.YAML, "name: shop-v1")

	m = c.response(c.send(MethodRollback, RevisionParams{Namespace: "shop", Application: "shop", Revision: "shop-v1"}, true))
	require.Empty(t, string(m["error"]))
	assert.Equal(t, []string{"shop/shop@shop-v1"}, rolled)
}
