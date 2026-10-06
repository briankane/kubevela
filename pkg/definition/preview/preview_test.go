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

package preview

import (
	"context"
	"os"
	"testing"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestMain(m *testing.M) {
	// The tests run without a cluster.
	cuex.EnableExternalPackageForDefaultCompiler = false
	cuex.EnableExternalPackageWatchForDefaultCompiler = false
	os.Exit(m.Run())
}

const webComponent = `"web": {
	type: "component"
	attributes: workload: definition: {apiVersion: "apps/v1", kind: "Deployment"}
}
template: {
	output: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: name: context.name
		spec: {
			replicas: parameter.replicas
			template: spec: containers: [{name: context.name, image: parameter.image}]
		}
	}
	outputs: service: {
		apiVersion: "v1"
		kind:       "Service"
		metadata: name: context.name + "-svc"
		spec: ports: [{port: parameter.port}]
	}
	parameter: {
		// +usage=Image to run
		image: string
		replicas: *1 | int
		port: *80 | int
		labels?: [string]: string
	}
}
`

const scalerTrait = `"scaler": {
	type: "trait"
	attributes: appliesToWorkloads: ["deployments.apps"]
}
template: {
	patch: spec: replicas: parameter.replicas
	outputs: hpa: {
		apiVersion: "autoscaling/v2"
		kind:       "HorizontalPodAutoscaler"
		metadata: name: context.name
		spec: maxReplicas: parameter.replicas + 4
	}
	parameter: replicas: *1 | int
}
`

// object decodes one rendered object's YAML.
func object(t *testing.T, o Object) map[string]interface{} {
	var m map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(o.YAML), &m))
	return m
}

func TestRenderComponent(t *testing.T) {
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte(`
parameter:
  image: nginx:1.25
  replicas: 3
context:
  name: shop-web
`)})
	require.Empty(t, r.Error)
	assert.Equal(t, "component", r.Type)
	require.Len(t, r.Objects, 2)

	assert.Equal(t, "output", r.Objects[0].Name)
	deploy := object(t, r.Objects[0])
	assert.Equal(t, "Deployment", deploy["kind"])
	assert.Equal(t, "shop-web", deploy["metadata"].(map[string]interface{})["name"], "context.name comes from the values")
	assert.EqualValues(t, 3, deploy["spec"].(map[string]interface{})["replicas"])

	assert.Equal(t, "outputs.service", r.Objects[1].Name)
	svc := object(t, r.Objects[1])
	assert.Equal(t, "shop-web-svc", svc["metadata"].(map[string]interface{})["name"])
	assert.Contains(t, r.Objects[1].YAML, "port: 80", "a default fills what the values leave out")
}

func TestRenderUsesSampleContext(t *testing.T) {
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte("parameter: {image: nginx}\n")})
	require.Empty(t, r.Error)
	assert.Equal(t, "web", object(t, r.Objects[0])["metadata"].(map[string]interface{})["name"], "context.name defaults to the definition's name")
}

func TestRenderNamesAPlaceholderStillInTheSkeleton(t *testing.T) {
	s, err := Skeleton(context.Background(), "web.cue", []byte(webComponent))
	require.NoError(t, err)
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte(s)})
	assert.Contains(t, r.Error, "image")
}

func TestRenderNamesAMissingParameter(t *testing.T) {
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte("parameter: {}\n")})
	assert.Contains(t, r.Error, "image")
	assert.Empty(t, r.Objects)
}

func TestRenderTraitPatchesASampleWorkload(t *testing.T) {
	r := Render(context.Background(), Request{Path: "scaler.cue", Source: []byte(scalerTrait), Values: []byte("parameter: {replicas: 4}\n")})
	require.Empty(t, r.Error)
	assert.Equal(t, "trait", r.Type)
	require.Len(t, r.Objects, 1)
	assert.Equal(t, "outputs.hpa", r.Objects[0].Name)
	assert.EqualValues(t, 8, object(t, r.Objects[0])["spec"].(map[string]interface{})["maxReplicas"])

	require.NotNil(t, r.Workload, "the workload the patch was applied to")
	w := object(t, *r.Workload)
	assert.Equal(t, "Deployment", w["kind"])
	assert.EqualValues(t, 4, w["spec"].(map[string]interface{})["replicas"])
}

func TestRenderTraitPatchesTheGivenWorkload(t *testing.T) {
	r := Render(context.Background(), Request{Path: "scaler.cue", Source: []byte(scalerTrait), Values: []byte(`
parameter: {replicas: 2}
workload:
  apiVersion: apps/v1
  kind: StatefulSet
  metadata: {name: db}
  spec: {serviceName: db}
`)})
	require.Empty(t, r.Error)
	w := object(t, *r.Workload)
	assert.Equal(t, "StatefulSet", w["kind"])
	assert.EqualValues(t, 2, w["spec"].(map[string]interface{})["replicas"])
	assert.Equal(t, "db", w["spec"].(map[string]interface{})["serviceName"])
}

func TestRenderSaysWhyAStepHasNoOutput(t *testing.T) {
	step := `"notify": {
	type: "workflow-step"
}
template: {
	parameter: url: string
}
`
	r := Render(context.Background(), Request{Path: "notify.cue", Source: []byte(step), Values: []byte("parameter: {url: x}\n")})
	assert.Empty(t, r.Error)
	assert.Empty(t, r.Objects)
	assert.Contains(t, r.Notice, "workflow step")
}

func TestRenderOfAFileThatIsNotADefinition(t *testing.T) {
	r := Render(context.Background(), Request{Path: "values.cue", Source: []byte("a: 1\n")})
	assert.Contains(t, r.Error, "not a definition")
}

func TestSkeleton(t *testing.T) {
	s, err := Skeleton(context.Background(), "web.cue", []byte(webComponent))
	require.NoError(t, err)
	var v map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(s), &v), "the skeleton is valid YAML:\n%s", s)
	p := v["parameter"].(map[string]interface{})
	assert.EqualValues(t, 1, p["replicas"])
	assert.EqualValues(t, 80, p["port"])
	assert.Contains(t, p, "image")
	assert.NotContains(t, p, "labels", "optional fields are left out")
	assert.Contains(t, s, "image: null # required string: Image to run", "a placeholder no render accepts, so the preview names what is missing")
	assert.Equal(t, "web", v["context"].(map[string]interface{})["name"])
	assert.NotContains(t, v, "workload", "only a trait patches a workload")

	trait, err := Skeleton(context.Background(), "scaler.cue", []byte(scalerTrait))
	require.NoError(t, err)
	assert.Contains(t, trait, "workload:", "a trait's skeleton carries the sample workload it patches")
}

// A values file of several documents renders each on its own.
func TestRenderEachInput(t *testing.T) {
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte(`name: small
parameter: {image: nginx:1.25, replicas: 1}
---
name: large
parameter: {image: nginx:1.25, replicas: 5}
---
parameter: {replicas: 2}
`)})
	require.Empty(t, r.Error)
	assert.Empty(t, r.Objects, "each input carries its own objects")
	require.Len(t, r.Inputs, 3)

	assert.Equal(t, "small", r.Inputs[0].Name)
	assert.EqualValues(t, 1, object(t, r.Inputs[0].Objects[0])["spec"].(map[string]interface{})["replicas"])
	assert.Equal(t, "large", r.Inputs[1].Name)
	assert.EqualValues(t, 5, object(t, r.Inputs[1].Objects[0])["spec"].(map[string]interface{})["replicas"])

	assert.Equal(t, "input 3", r.Inputs[2].Name, "an input without a name is numbered")
	assert.Contains(t, r.Inputs[2].Error, "image", "one input failing leaves the others")
}

func TestRenderOneInputIsUnchanged(t *testing.T) {
	r := Render(context.Background(), Request{Path: "web.cue", Source: []byte(webComponent), Values: []byte("parameter: {image: nginx:1.25}\n---\n")})
	assert.Empty(t, r.Inputs)
	assert.Len(t, r.Objects, 2)
}
