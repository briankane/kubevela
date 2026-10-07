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
	"context"
	"encoding/json"
	"fmt"

	wfdebug "github.com/kubevela/workflow/pkg/debug"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// debugStep is one workflow step's debug data: the ConfigMap the debug policy
// writes it to, and, once read, its CUE or why there is none.
type debugStep struct {
	Name      string `json:"name"`
	Phase     string `json:"phase,omitempty"`
	ConfigMap string `json:"configMap"`
	Debug     string `json:"debug,omitempty"`
	Error     string `json:"error,omitempty"`
}

// noDebugData are the step types the debug policy records nothing for.
var noDebugData = map[string]bool{"suspend": true, "step-group": true}

// debugConfigMaps are the ConfigMaps holding an Application's steps' debug
// data, sub-steps included, for its current workflow run: each is named for
// the Application, the step's id and its UID, as the workflow engine names it.
func debugConfigMaps(app unstructured.Unstructured) []debugStep {
	var out []debugStep
	var each func(steps []interface{})
	each = func(steps []interface{}) {
		for _, raw := range steps {
			step, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			str := func(k string) string { s, _ := step[k].(string); return s }
			if !noDebugData[str("type")] && str("id") != "" {
				out = append(out, debugStep{
					Name:      str("name"),
					Phase:     str("phase"),
					ConfigMap: wfdebug.GenerateContextName(app.GetName(), str("id"), string(app.GetUID())),
				})
			}
			if subs, ok := step["subSteps"].([]interface{}); ok {
				each(subs)
			}
		}
	}
	steps, _, _ := unstructured.NestedSlice(app.Object, "status", "workflow", "steps")
	each(steps)
	return out
}

// readDebugData reads an Application's steps' debug data from the cluster.
func readDebugData(cfg *rest.Config, namespace, name string) ([]debugStep, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*clusterTimeout)
	defer cancel()
	app, err := client.Resource(v1beta1.SchemeGroupVersion.WithResource("applications")).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	steps := debugConfigMaps(*app)
	cms := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace)
	for i := range steps {
		cm, err := cms.Get(ctx, steps[i].ConfigMap, metav1.GetOptions{})
		if err != nil {
			steps[i].Error = fmt.Sprintf("no debug data: %v", err)
			continue
		}
		data, _, _ := unstructured.NestedString(cm.Object, "data", "debug")
		if data == "" {
			steps[i].Error = "the debug data is empty"
			continue
		}
		steps[i].Debug = data
	}
	return steps, nil
}

// MethodDebugData reads an Application's workflow steps' debug data, as its
// debug policy recorded it. It is this server's own request.
const MethodDebugData = "vela/debugData"

// DebugDataParams name the Application.
type DebugDataParams struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// DebugDataResult is each step's debug data, in the order the steps ran, and
// the context it was read from.
type DebugDataResult struct {
	Context string      `json:"context"`
	Steps   []debugStep `json:"steps"`
}

// debugData answers MethodDebugData off the message loop, as it reads the
// cluster.
func (s *Server) debugData(id json.RawMessage, p DebugDataParams) {
	fail := func(msg string) {
		_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: msg})
	}
	switch {
	case !s.clusterEnabled:
		fail("reading the cluster is off (kubevela.readCluster)")
		return
	case s.cluster == nil || s.cluster.err != nil || s.cluster.debugData == nil:
		fail("the cluster was not reached")
		return
	}
	read, kubeContext := s.cluster.debugData, s.cluster.context
	go func() {
		steps, err := read(p.Namespace, p.Name)
		s.post(func() {
			if err != nil {
				fail(fmt.Sprintf("could not read %s/%s on %s: %v", p.Namespace, p.Name, kubeContext, err))
				return
			}
			if steps == nil {
				steps = []debugStep{}
			}
			_ = s.reply(id, DebugDataResult{Context: kubeContext, Steps: steps}, nil)
		})
	}()
}
