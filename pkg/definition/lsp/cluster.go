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
	"fmt"
	"strings"
	"time"

	"github.com/kubevela/pkg/apis/cue/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

// Cluster is what the editor reads of the kubeconfig's cluster.
type Cluster struct {
	// Fetch fetches the OpenAPI v3 document of one of its group-versions.
	Fetch kubeschema.Fetch
	// KubeVela is set when it runs KubeVela.
	KubeVela bool
	// Context is the kubeconfig context reached.
	Context string
	// Packages are its Package resources, in every namespace, as the
	// controller loads them.
	Packages []v1alpha1.Package
	// Definition reads the definition of a kind and name applied to it,
	// and the namespace it is in.
	Definition func(kind, name string) (*unstructured.Unstructured, string, error)
	// Definitions are the definitions an Application may name: its
	// components, traits, policies and workflow steps, in every namespace.
	Definitions []unstructured.Unstructured
	// DebugData reads an Application's workflow steps' debug data.
	DebugData func(namespace, name string) ([]debugStep, error)
	// RevisionDefinition reads a definition as an Application's current
	// revision recorded it, and names the revision.
	RevisionDefinition func(namespace, app, typ, name string) (*unstructured.Unstructured, string, error)
	// Watch calls each with an Application as it changes, nil once it is
	// deleted, until ctx is done.
	Watch func(ctx context.Context, namespace, name string, each func(*unstructured.Unstructured))
	// Resource reads a resource as YAML.
	Resource func(cluster, apiVersion, kind, namespace, name string) (string, error)
	// Events calls each with every event in a namespace as they change,
	// until ctx is done.
	Events func(ctx context.Context, namespace string, each func([]*unstructured.Unstructured))
	// Logs calls each with the lines the workloads' pods write, until ctx is
	// done.
	Logs func(ctx context.Context, namespace string, workloads []LogWorkload, each func([]LogLine))
	// UISchema reads a definition's UI schema, by its ConfigMap's name.
	UISchema func(name string) (string, error)
	// Applications calls each with every Application on the cluster as they
	// change, until ctx is done.
	Applications func(ctx context.Context, each func([]*unstructured.Unstructured))
	// Revisions lists an Application's revisions, newest first.
	Revisions func(namespace, app string) ([]RevisionInfo, error)
	// RevisionApplication reads the Application a revision recorded, as YAML.
	RevisionApplication func(namespace, revision string) (string, error)
	// Rollback gives an Application the spec one of its revisions recorded.
	Rollback func(namespace, app, revision string) error
	// ConfigTemplateSources are its config templates' CUE, for checking the
	// Configs that name them.
	ConfigTemplateSources []analysis.ConfigTemplate
	// Configs lists its config templates and configs.
	Configs func() (ConfigsResult, error)
	// ConfigTemplate reads what a config template's form is made from.
	ConfigTemplate func(namespace, name string) (ConfigTemplateResult, error)
	// ConfigProperties reads a config's values.
	ConfigProperties func(namespace, name string) (ConfigPropertiesResult, error)
	// Clusters lists the clusters joined to it; Joined is that list as it
	// was when it was reached.
	Clusters func() ([]ManagedCluster, error)
	Joined   []ManagedCluster
	// Controller is its KubeVela controller as its Deployment runs it, none
	// when there is none; ReadController reads it again.
	Controller     *ControllerInfo
	ReadController func() (*ControllerInfo, error)
}

// ClusterConnector reaches the cluster the kubeconfig names.
type ClusterConnector func() (Cluster, error)

// clusterTimeout bounds each call to the cluster, so an unreachable one
// cannot hold an editor up.
const clusterTimeout = 3 * time.Second

// kubeVelaGroup is the API group a cluster running KubeVela serves.
const kubeVelaGroup = "core.oam.dev"

// ConnectKubeconfig is the ClusterConnector for the kubeconfig's current
// context, as kubectl reads it.
func ConnectKubeconfig() (Cluster, error) {
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	raw, err := cc.RawConfig()
	if err != nil {
		return Cluster{}, err
	}
	out := Cluster{Context: raw.CurrentContext}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return out, err
	}
	cfg.Timeout = clusterTimeout
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return out, err
	}
	groups, err := dc.ServerGroups()
	if err != nil {
		return out, err
	}
	packages := false
	for _, g := range groups.Groups {
		out.KubeVela = out.KubeVela || g.Name == kubeVelaGroup
		packages = packages || g.Name == v1alpha1.GroupVersion.Group
	}
	if packages {
		out.Packages = listPackages(cfg)
	}
	out.Definition = func(kind, name string) (*unstructured.Unstructured, string, error) {
		return getDefinition(cfg, kind, name)
	}
	out.DebugData = func(namespace, name string) ([]debugStep, error) {
		return readDebugData(cfg, namespace, name)
	}
	out.RevisionDefinition = func(namespace, app, typ, name string) (*unstructured.Unstructured, string, error) {
		return readRevisionDefinition(cfg, namespace, app, typ, name)
	}
	out.Watch = func(ctx context.Context, namespace, name string, each func(*unstructured.Unstructured)) {
		watchApplication(ctx, cfg, namespace, name, each)
	}
	out.Resource = func(cluster, apiVersion, kind, namespace, name string) (string, error) {
		return readResource(clusterConfig(cfg, cluster), apiVersion, kind, namespace, name)
	}
	out.Events = func(ctx context.Context, namespace string, each func([]*unstructured.Unstructured)) {
		watchEvents(ctx, cfg, namespace, each)
	}
	out.Logs = func(ctx context.Context, namespace string, workloads []LogWorkload, each func([]LogLine)) {
		followLogs(ctx, cfg, namespace, workloads, each)
	}
	out.UISchema = func(name string) (string, error) {
		return readUISchema(cfg, name)
	}
	out.Applications = func(ctx context.Context, each func([]*unstructured.Unstructured)) {
		watchApplications(ctx, cfg, each)
	}
	out.Revisions = func(namespace, app string) ([]RevisionInfo, error) {
		return listRevisions(cfg, namespace, app)
	}
	out.RevisionApplication = func(namespace, revision string) (string, error) {
		return readRevisionApplication(cfg, namespace, revision)
	}
	out.Rollback = func(namespace, app, revision string) error {
		return rollback(cfg, namespace, app, revision)
	}
	out.Configs = func() (ConfigsResult, error) {
		return withConfigClient(cfg, readConfigs)
	}
	out.ConfigTemplate = func(namespace, name string) (ConfigTemplateResult, error) {
		return withConfigClient(cfg, func(ctx context.Context, cli ctrlclient.Client) (ConfigTemplateResult, error) {
			return readConfigTemplate(ctx, cli, namespace, name)
		})
	}
	out.ConfigProperties = func(namespace, name string) (ConfigPropertiesResult, error) {
		return withConfigClient(cfg, func(ctx context.Context, cli ctrlclient.Client) (ConfigPropertiesResult, error) {
			return readConfigProperties(ctx, cli, namespace, name)
		})
	}
	out.Clusters = func() ([]ManagedCluster, error) {
		return withConfigClient(cfg, readClusters)
	}
	if !out.KubeVela {
		return out, nil
	}
	out.Definitions = listDefinitions(cfg)
	out.ConfigTemplateSources = listConfigTemplateSources(cfg)
	out.ReadController = func() (*ControllerInfo, error) {
		return withConfigClient(cfg, readController)
	}
	out.Controller, _ = out.ReadController()
	out.Joined, _ = out.Clusters()
	paths, err := dc.OpenAPIV3().Paths()
	if err != nil {
		return out, err
	}
	out.Fetch = func(gv string) ([]byte, error) {
		key := "api/" + gv
		if strings.Contains(gv, "/") {
			key = "apis/" + gv
		}
		p, ok := paths[key]
		if !ok {
			return nil, fmt.Errorf("the cluster serves no %s", gv)
		}
		return p.Schema("application/json")
	}
	return out, nil
}

// listPackages are the cluster's Package resources, in every namespace; a
// resource that does not decode is passed over, as the controller passes it.
func listPackages(cfg *rest.Config) []v1alpha1.Package {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterTimeout)
	defer cancel()
	list, err := client.Resource(v1alpha1.PackageGroupVersionResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var out []v1alpha1.Package
	for _, item := range list.Items {
		var p v1alpha1.Package
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// definitionNamespace is where vela def apply puts a definition by default.
const definitionNamespace = "vela-system"

// getDefinition reads the definition of a kind and name: from vela-system,
// where vela def apply puts one by default, or else from any namespace.
func getDefinition(cfg *rest.Config, kind, name string) (*unstructured.Unstructured, string, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, "", err
	}
	gvr := v1beta1.SchemeGroupVersion.WithResource(strings.ToLower(kind) + "s")
	ctx, cancel := context.WithTimeout(context.Background(), clusterTimeout)
	defer cancel()
	if obj, err := client.Resource(gvr).Namespace(definitionNamespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return obj, definitionNamespace, nil
	}
	list, err := client.Resource(gvr).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name})
	if err != nil {
		return nil, "", err
	}
	if len(list.Items) == 0 {
		return nil, "", fmt.Errorf("no %s named %s is applied", kind, name)
	}
	obj := list.Items[0]
	return &obj, obj.GetNamespace(), nil
}

// appDefinitionKinds are the kinds of definition an Application names.
var appDefinitionKinds = []string{"ComponentDefinition", "TraitDefinition", "PolicyDefinition", "WorkflowStepDefinition"}

// listDefinitions are the cluster's definitions of the kinds an Application
// names, in every namespace; a kind it cannot list is passed over.
func listDefinitions(cfg *rest.Config) []unstructured.Unstructured {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil
	}
	var out []unstructured.Unstructured
	for _, kind := range appDefinitionKinds {
		ctx, cancel := context.WithTimeout(context.Background(), clusterTimeout)
		list, err := client.Resource(v1beta1.SchemeGroupVersion.WithResource(strings.ToLower(kind)+"s")).List(ctx, metav1.ListOptions{})
		cancel()
		if err == nil {
			out = append(out, list.Items...)
		}
	}
	return out
}
