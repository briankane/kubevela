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
	"sort"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	wfTypes "github.com/kubevela/workflow/pkg/types"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// MethodWatchApplications starts sending every Application on the cluster,
// summarised, as MethodApplicationsChanged notifications whenever one
// changes, until MethodUnwatchApplications. It is this server's own request.
const MethodWatchApplications = "vela/watchApplications"

// MethodUnwatchApplications ends the watch MethodWatchApplications started.
const MethodUnwatchApplications = "vela/unwatchApplications"

// MethodApplicationsChanged is the server's notification of the cluster's
// Applications, as they are now.
const MethodApplicationsChanged = "vela/applicationsChanged"

// applicationsKey is the key of the watch of every Application.
const applicationsKey = "applications"

// applicationsSettle is how long a change waits for others before the
// Applications are sent, so a burst of them is sent once.
const applicationsSettle = 200 * time.Millisecond

// AppSummary is what a list of Applications shows of one.
type AppSummary struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	Components int    `json:"components,omitempty"`
	// Healthy is whether every component is healthy, and there is one.
	Healthy    bool `json:"healthy,omitempty"`
	Suspended  bool `json:"suspended,omitempty"`
	Terminated bool `json:"terminated,omitempty"`
	Finished   bool `json:"finished,omitempty"`
	Deleting   bool `json:"deleting,omitempty"`
	// Step is the step that failed, else the one waiting, else the one
	// running, and StepPhase its phase. A step stopped by terminating the
	// workflow is not one that failed.
	Step      string `json:"step,omitempty"`
	StepPhase string `json:"stepPhase,omitempty"`
	Message   string `json:"message,omitempty"`
	// Addon is the addon that installed it, if one did.
	Addon string `json:"addon,omitempty"`
	// Clusters is how many clusters it runs on, when more than one, and
	// HealthyClusters how many of them have every component healthy.
	Clusters        int `json:"clusters,omitempty"`
	HealthyClusters int `json:"healthyClusters,omitempty"`
}

// ApplicationsChanged is every Application on the cluster, summarised and
// sorted by namespace and name.
type ApplicationsChanged struct {
	Context      string       `json:"context"`
	Applications []AppSummary `json:"applications"`
}

// summarize is what a list shows of an Application.
func summarize(app *unstructured.Unstructured) AppSummary {
	out := AppSummary{Namespace: app.GetNamespace(), Name: app.GetName(), Deleting: app.GetDeletionTimestamp() != nil, Addon: app.GetLabels()["addons.oam.dev/name"]}
	out.Phase, _, _ = unstructured.NestedString(app.Object, "status", "status")
	services, _, _ := unstructured.NestedSlice(app.Object, "status", "services")
	out.Components = len(services)
	out.Healthy = len(services) > 0
	clusterHealthy := map[string]bool{}
	for _, s := range services {
		m, ok := s.(map[string]interface{})
		healthy := ok && m["healthy"] == true
		if !healthy {
			out.Healthy = false
		}
		cluster, _ := m["cluster"].(string)
		if cluster == "" {
			cluster = "local"
		}
		if was, seen := clusterHealthy[cluster]; !seen || was {
			clusterHealthy[cluster] = healthy
		}
	}
	if len(clusterHealthy) > 1 {
		out.Clusters = len(clusterHealthy)
		for _, h := range clusterHealthy {
			if h {
				out.HealthyClusters++
			}
		}
	}
	wf, _, _ := unstructured.NestedMap(app.Object, "status", "workflow")
	out.Suspended, _, _ = unstructured.NestedBool(wf, "suspend")
	out.Terminated, _, _ = unstructured.NestedBool(wf, "terminated")
	out.Finished, _, _ = unstructured.NestedBool(wf, "finished")
	out.Message, _, _ = unstructured.NestedString(wf, "message")
	steps, _, _ := unstructured.NestedSlice(wf, "steps")
	for _, phase := range []string{"failed", "suspending", "running"} {
		for _, s := range steps {
			m, _ := s.(map[string]interface{})
			if m["phase"] == phase && m["reason"] != wfTypes.StatusReasonTerminate {
				out.Step, _ = m["name"].(string)
				out.StepPhase = phase
				return out
			}
		}
	}
	return out
}

// watchApplications calls each with every Application on the cluster, then
// again whenever one changes, until ctx is done.
func watchApplications(ctx context.Context, cfg *rest.Config, each func([]*unstructured.Unstructured)) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return
	}
	apps := client.Resource(v1beta1.SchemeGroupVersion.WithResource("applications"))
	for ctx.Err() == nil {
		list, err := apps.List(ctx, metav1.ListOptions{})
		if err == nil {
			byUID := map[string]*unstructured.Unstructured{}
			for i := range list.Items {
				byUID[string(list.Items[i].GetUID())] = &list.Items[i]
			}
			send := func() {
				all := make([]*unstructured.Unstructured, 0, len(byUID))
				for _, app := range byUID {
					all = append(all, app)
				}
				each(all)
			}
			send()
			if w, err := apps.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion()}); err == nil {
				for ev := range w.ResultChan() {
					obj, ok := ev.Object.(*unstructured.Unstructured)
					if !ok {
						continue
					}
					switch ev.Type {
					case watch.Added, watch.Modified:
						byUID[string(obj.GetUID())] = obj
					case watch.Deleted:
						delete(byUID, string(obj.GetUID()))
					default:
						continue
					}
					send()
				}
				w.Stop()
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(watchRetry):
		}
	}
}

// startApplicationsWatch answers MethodWatchApplications: it ends any watch
// of them, then watches them off the message loop, sending them once a
// burst of changes settles.
func (s *Server) startApplicationsWatch() *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.applications == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	s.stopKey(applicationsKey)
	ctx, cancel := context.WithCancel(context.Background())
	if s.watches == nil {
		s.watches = map[string]context.CancelFunc{}
	}
	s.watches[applicationsKey] = cancel
	kubeContext := s.cluster.context
	var mu sync.Mutex
	var latest []*unstructured.Unstructured
	pending := false
	flush := func() {
		mu.Lock()
		all := latest
		pending = false
		mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		change := ApplicationsChanged{Context: kubeContext, Applications: make([]AppSummary, 0, len(all))}
		for _, app := range all {
			change.Applications = append(change.Applications, summarize(app))
		}
		sort.Slice(change.Applications, func(i, j int) bool {
			a, b := change.Applications[i], change.Applications[j]
			return a.Namespace < b.Namespace || a.Namespace == b.Namespace && a.Name < b.Name
		})
		_ = s.write(message{JSONRPC: "2.0", Method: MethodApplicationsChanged, Params: mustJSON(change)})
	}
	watchFn := s.cluster.applications
	go watchFn(ctx, func(all []*unstructured.Unstructured) {
		mu.Lock()
		defer mu.Unlock()
		latest = all
		if !pending {
			pending = true
			time.AfterFunc(applicationsSettle, flush)
		}
	})
	return nil
}
