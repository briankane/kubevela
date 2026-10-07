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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

// MethodResource reads a resource an Application applied, as YAML. It is this
// server's own request.
const MethodResource = "vela/resource"

// ResourceParams name the resource as an Application's appliedResources do.
type ResourceParams struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	Cluster    string `json:"cluster,omitempty"`
}

// ResourceResult is the resource as YAML, and the context it was read from.
type ResourceResult struct {
	YAML    string `json:"yaml"`
	Context string `json:"context"`
}

// resourceYAML is a resource as YAML, without the managed fields no one
// reading it wants.
func resourceYAML(obj *unstructured.Unstructured) (string, error) {
	obj = obj.DeepCopy()
	obj.SetManagedFields(nil)
	out, err := yaml.Marshal(obj.Object)
	return string(out), err
}

// readResource reads a resource by its kind, mapped to its API resource as
// the cluster serves it.
func readResource(cfg *rest.Config, apiVersion, kind, namespace, name string) (string, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return "", err
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", err
	}
	mapping, err := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)).RESTMapping(gv.WithKind(kind).GroupKind(), gv.Version)
	if err != nil {
		return "", err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTimeout)
	defer cancel()
	var obj *unstructured.Unstructured
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		obj, err = client.Resource(mapping.Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = client.Resource(mapping.Resource).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return "", err
	}
	return resourceYAML(obj)
}

// resource answers MethodResource off the message loop. A member cluster's
// resource goes through cluster-gateway, which this server does not.
func (s *Server) resource(id json.RawMessage, p ResourceParams) {
	fail := func(msg string) {
		_ = s.reply(id, nil, &ResponseError{Code: CodeInvalidParams, Message: msg})
	}
	switch {
	case p.Cluster != "" && p.Cluster != "local":
		fail(fmt.Sprintf("%s %s is on cluster %s, which is read through cluster-gateway: vela status --tree", p.Kind, p.Name, p.Cluster))
		return
	case !s.clusterEnabled:
		fail("reading the cluster is off (kubevela.readCluster)")
		return
	case s.cluster == nil || s.cluster.err != nil || s.cluster.resource == nil:
		fail("the cluster was not reached")
		return
	}
	read, kubeContext := s.cluster.resource, s.cluster.context
	go func() {
		out, err := read(p.APIVersion, p.Kind, p.Namespace, p.Name)
		s.post(func() {
			if err != nil {
				fail(err.Error())
				return
			}
			_ = s.reply(id, ResourceResult{YAML: out, Context: kubeContext}, nil)
		})
	}()
}
