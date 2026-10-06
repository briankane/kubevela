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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

const deploymentTypo = `"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: output: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	spec: replicass: 2
}
`

const gadgetDef = `"gadget": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: output: {
	apiVersion: "example.com/v1alpha1"
	kind:       "Gadget"
	spec: colour: "red"
}
`

const gadgetCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: gadgets.example.com}
spec:
  group: example.com
  names: {kind: Gadget, plural: gadgets}
  scope: Namespaced
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec: {type: object, properties: {color: {type: string}}}
`

// noCluster is a kube context that reaches nothing.
func noCluster() (kubeschema.Fetch, bool, string, error) {
	return nil, false, "", errors.New("no cluster")
}

// velaCluster is a cluster with KubeVela that serves the Gadget kind.
func velaCluster() (kubeschema.Fetch, bool, string, error) {
	return func(gv string) ([]byte, error) {
		if gv == "example.com/v1alpha1" {
			return []byte(`{"components":{"schemas":{"com.example.v1alpha1.Gadget":{"type":"object","properties":{"apiVersion":{"type":"string"},"kind":{"type":"string"},"spec":{"type":"object","properties":{"color":{"type":"string"}}}},"x-kubernetes-group-version-kind":[{"group":"example.com","version":"v1alpha1","kind":"Gadget"}]}}}}`), nil
		}
		return nil, errors.New("not served")
	}, true, "k3d-test", nil
}

// openWith opens a document on a server reaching cluster, with the setting
// mode, and returns its diagnostics once the server has settled.
func openWith(t *testing.T, cluster ClusterConnector, mode, root, name, src string) (*client, string, []Diagnostic) {
	t.Helper()
	c := newClientWith(t, NewServer(WithCluster(cluster)))
	params := map[string]interface{}{"initializationOptions": map[string]interface{}{"validateOutputs": mode}}
	if root != "" {
		params["rootUri"] = "file://" + root
	}
	c.response(c.send("initialize", params, true))
	c.send("initialized", map[string]interface{}{}, false)
	if root == "" {
		root = t.TempDir()
	}
	u := "file://" + filepath.Join(root, name)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: u, LanguageID: "cue", Version: 1, Text: src}}, false)
	return c, u, settled(c, u)
}

// settled is the last diagnostics published for uri before the server goes
// quiet: the index and the cluster are reached in the background, and each
// may publish again.
func settled(c *client, uri string) []Diagnostic {
	var last []Diagnostic
	for {
		m, ok := c.tryRead(time.Second)
		if !ok {
			return last
		}
		var p PublishDiagnosticsParams
		if string(m["method"]) == `"textDocument/publishDiagnostics"` && json.Unmarshal(m["params"], &p) == nil && p.URI == uri {
			last = p.Diagnostics
		}
	}
}

func TestValidateOutputsModes(t *testing.T) {
	caught := func(d []Diagnostic) bool { return strings.Contains(messages(d), "replicass") }

	_, _, d := openWith(t, noCluster, "off", "", "web.cue", deploymentTypo)
	assert.False(t, caught(d), "off checks nothing against kinds")

	_, _, d = openWith(t, noCluster, "on", "", "web.cue", deploymentTypo)
	assert.True(t, caught(d), "on checks against the built-in kinds")

	_, _, d = openWith(t, noCluster, "auto", "", "web.cue", deploymentTypo)
	assert.False(t, caught(d), "auto without a KubeVela cluster is off")

	_, _, d = openWith(t, velaCluster, "auto", "", "web.cue", deploymentTypo)
	assert.True(t, caught(d), "auto with a KubeVela cluster is on")
}

func TestValidateOutputsSources(t *testing.T) {
	colour := func(d []Diagnostic) bool { return strings.Contains(messages(d), "colour") }

	_, _, d := openWith(t, velaCluster, "auto", "", "gadget.cue", gadgetDef)
	assert.True(t, colour(d), "a kind the cluster serves")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gadget-crd.yaml"), []byte(gadgetCRDYAML), 0o600))
	_, _, d = openWith(t, noCluster, "on", dir, "gadget.cue", gadgetDef)
	assert.True(t, colour(d), "a CRD in the workspace")
}

func TestValidateOutputsFollowsTheSetting(t *testing.T) {
	caught := func(d []Diagnostic) bool { return strings.Contains(messages(d), "replicass") }
	c, u, d := openWith(t, noCluster, "on", "", "web.cue", deploymentTypo)
	require.True(t, caught(d))
	c.send("workspace/didChangeConfiguration", map[string]interface{}{"settings": map[string]interface{}{"kubevela": map[string]interface{}{"validateOutputs": "off"}}}, false)
	assert.False(t, caught(settled(c, u)))
}
