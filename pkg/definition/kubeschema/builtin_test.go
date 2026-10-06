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

package kubeschema

import (
	"bytes"
	"compress/gzip"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var update = flag.Bool("update", false, "capture builtin/ from envtest's kube-apiserver (needs KUBEBUILDER_ASSETS)")

// builtinPaths are the /openapi/v3 documents bundled: the API groups a
// definition's output is most often of.
var builtinPaths = []string{
	"api/v1",
	"apis/apps/v1",
	"apis/batch/v1",
	"apis/networking.k8s.io/v1",
	"apis/policy/v1",
	"apis/autoscaling/v2",
	"apis/rbac.authorization.k8s.io/v1",
	"apis/storage.k8s.io/v1",
}

// TestBuiltinDocuments checks the bundled documents load and hold the kinds
// definitions most often output. With -update it first captures them again
// from envtest's kube-apiserver; use the Kubernetes version of k8s.io/api in
// go.mod, so the schemas match the types KubeVela is built against.
func TestBuiltinDocuments(t *testing.T) {
	if *update {
		captureBuiltin(t)
	}
	s, err := Builtin()
	require.NoError(t, err)
	for _, gvk := range []GVK{
		{Version: "v1", Kind: "Service"},
		{Version: "v1", Kind: "ConfigMap"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		{Group: "batch", Version: "v1", Kind: "CronJob"},
		{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
		{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
		{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
	} {
		assert.True(t, s.Has(gvk), "%v", gvk)
	}
	for resource, gvk := range builtinResources {
		assert.True(t, s.Has(gvk), "%s names %v, which is not bundled", resource, gvk)
	}

	deployment := GVK{Group: "apps", Version: "v1", Kind: "Deployment"}
	assert.NoError(t, unify(t, s, deployment, `{apiVersion: "apps/v1", kind: "Deployment", metadata: name: "w", spec: {replicas: 2, template: spec: containers: [{name: "c", image: "nginx", imagePullPolicy: "Always", ports: [{containerPort: 80}], resources: limits: memory: "1Gi"}]}}`))
	for value, bad := range map[string]string{
		`{spec: replicass: 2}`: "replicass",
		`{spec: template: spec: containers: [{name: "c", imagePullPolicy: "Sometimes"}]}`:   "imagePullPolicy",
		`{spec: template: spec: containers: [{name: "c", ports: [{containerPort: "80"}]}]}`: "containerPort",
	} {
		err := unify(t, s, deployment, value)
		require.Error(t, err, value)
		assert.Contains(t, err.Error(), bad)
	}
}

func captureBuiltin(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err, "start envtest: set KUBEBUILDER_ASSETS")
	defer func() { _ = env.Stop() }()
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	require.NoError(t, err)
	paths, err := dc.OpenAPIV3().Paths()
	require.NoError(t, err)
	version, err := dc.ServerVersion()
	require.NoError(t, err)

	old, _ := filepath.Glob("builtin/*.json.gz")
	for _, f := range old {
		require.NoError(t, os.Remove(f))
	}
	for _, p := range builtinPaths {
		gv, ok := paths[p]
		require.True(t, ok, "the API server serves no %s", p)
		doc, err := gv.Schema("application/json")
		require.NoError(t, err)
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, err = zw.Write(doc)
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		require.NoError(t, os.WriteFile(filepath.Join("builtin", strings.ReplaceAll(p, "/", "_")+".json.gz"), buf.Bytes(), 0o600))
	}
	require.NoError(t, os.WriteFile("builtin/VERSION", []byte(version.GitVersion+"\n"), 0o600))
}
