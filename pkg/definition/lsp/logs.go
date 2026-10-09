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
	"bufio"
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	pkgmulticluster "github.com/kubevela/pkg/multicluster"
)

// MethodWatchLogs starts sending the logs of an Application's pods as they
// are written, as MethodLogs notifications, until MethodUnwatchLogs. It is
// this server's own request.
const MethodWatchLogs = "vela/watchLogs"

// MethodUnwatchLogs ends a watch MethodWatchLogs started.
const MethodUnwatchLogs = "vela/unwatchLogs"

// MethodLogs is the server's notification of lines an Application's pods wrote.
const MethodLogs = "vela/logs"

// LogWorkload is a resource an Application applied whose pods' logs are its.
type LogWorkload struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	// Namespace is the workload's, where a policy put it in another than
	// the Application's.
	Namespace string `json:"namespace,omitempty"`
	// Cluster is the member cluster it is on; none for the hub.
	Cluster string `json:"cluster,omitempty"`
}

// WatchLogsParams name the Application, and the workloads it applied.
type WatchLogsParams struct {
	Namespace   string        `json:"namespace"`
	Application string        `json:"application"`
	Workloads   []LogWorkload `json:"workloads,omitempty"`
}

// LogLine is a line a pod's container wrote, and the component whose
// workload made the pod.
type LogLine struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Component string `json:"component,omitempty"`
	Text      string `json:"text"`
	// Cluster is the member cluster the pod is on; none for the hub.
	Cluster string `json:"cluster,omitempty"`
}

// LogsWritten are lines an Application's pods wrote since the last.
type LogsWritten struct {
	Namespace   string    `json:"namespace"`
	Application string    `json:"application"`
	Lines       []LogLine `json:"lines"`
}

const (
	// logTail is how many lines of each container's log are sent first.
	logTail = int64(100)
	// logFlush is how often the lines written are sent.
	logFlush = 250 * time.Millisecond
	// podRefresh is how often pods are looked for again, as a rollout makes
	// new ones.
	podRefresh = 5 * time.Second
)

// podKinds are the workloads whose pods are found by their selector.
var podKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true, "Job": true}

// podSelector is the label selector of a workload's pods.
func podSelector(obj unstructured.Unstructured) (string, bool) {
	if !podKinds[obj.GetKind()] {
		return "", false
	}
	raw, found, _ := unstructured.NestedMap(obj.Object, "spec", "selector")
	if !found {
		return "", false
	}
	var sel metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel); err != nil {
		return "", false
	}
	s, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil || s.Empty() {
		return "", false
	}
	return s.String(), true
}

// followLogs calls each with the lines an Application's workloads' pods
// write, batched, from the last logTail of each container, until ctx is done.
// Pods are looked for again every podRefresh; each container of a pod is
// followed once.
func followLogs(ctx context.Context, cfg *rest.Config, namespace string, workloads []LogWorkload, each func([]LogLine)) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	var mu sync.Mutex
	var pending []LogLine
	write := func(l LogLine) {
		mu.Lock()
		pending = append(pending, l)
		mu.Unlock()
	}
	go func() {
		t := time.NewTicker(logFlush)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				batch := pending
				pending = nil
				mu.Unlock()
				if len(batch) > 0 {
					each(batch)
				}
			}
		}
	}()

	following := map[string]bool{}
	follow := func(pod corev1.Pod, container, component string) {
		tail := logTail
		req := cs.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, Follow: true, TailLines: &tail})
		stream, err := req.Stream(ctx)
		if err != nil {
			return
		}
		defer func() { _ = stream.Close() }()
		scan := bufio.NewScanner(stream)
		scan.Buffer(make([]byte, 64*1024), 1024*1024)
		for scan.Scan() {
			write(LogLine{Pod: pod.Name, Container: container, Component: component, Text: scan.Text()})
		}
	}
	for ctx.Err() == nil {
		for _, w := range workloads {
			pods, component := podsOf(ctx, cs, dyn, mapper, namespace, w)
			for _, pod := range pods {
				if pod.Status.Phase == corev1.PodPending {
					continue
				}
				for _, c := range pod.Spec.Containers {
					key := string(pod.UID) + "/" + c.Name
					if following[key] {
						continue
					}
					following[key] = true
					go follow(pod, c.Name, component)
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(podRefresh):
		}
	}
}

// podsOf are a workload's pods, and the component its labels name.
func podsOf(ctx context.Context, cs kubernetes.Interface, dyn dynamic.Interface, mapper *restmapper.DeferredDiscoveryRESTMapper, namespace string, w LogWorkload) ([]corev1.Pod, string) {
	if w.Namespace != "" {
		namespace = w.Namespace
	}
	gv, err := schema.ParseGroupVersion(w.APIVersion)
	if err != nil {
		return nil, ""
	}
	if w.Kind == "Pod" {
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return nil, ""
		}
		return []corev1.Pod{*pod}, pod.Labels["app.oam.dev/component"]
	}
	if !podKinds[w.Kind] {
		return nil, ""
	}
	mapping, err := mapper.RESTMapping(gv.WithKind(w.Kind).GroupKind(), gv.Version)
	if err != nil {
		return nil, ""
	}
	obj, err := dyn.Resource(mapping.Resource).Namespace(namespace).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return nil, ""
	}
	sel, ok := podSelector(*obj)
	if !ok {
		return nil, ""
	}
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, ""
	}
	return list.Items, obj.GetLabels()["app.oam.dev/component"]
}

// logsKey is the key of an Application's logs watch.
func logsKey(namespace, app string) string {
	return "logs:" + namespace + "/" + app
}

// startLogsWatch answers MethodWatchLogs: it ends any watch of the same
// Application's logs, then follows its pods' off the message loop.
func (s *Server) startLogsWatch(p WatchLogsParams) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.logs == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	key := logsKey(p.Namespace, p.Application)
	s.stopKey(key)
	ctx, cancel := context.WithCancel(context.Background())
	if s.watches == nil {
		s.watches = map[string]context.CancelFunc{}
	}
	s.watches[key] = cancel
	followFn := s.cluster.logs
	byCluster := map[string][]LogWorkload{}
	for _, w := range p.Workloads {
		cluster := w.Cluster
		if cluster == "" {
			cluster = pkgmulticluster.Local
		}
		byCluster[cluster] = append(byCluster[cluster], w)
	}
	for cluster, workloads := range byCluster {
		go followFn(ctx, cluster, p.Namespace, workloads, func(lines []LogLine) {
			if ctx.Err() != nil {
				return
			}
			if cluster != pkgmulticluster.Local {
				for i := range lines {
					lines[i].Cluster = cluster
				}
			}
			_ = s.write(message{JSONRPC: "2.0", Method: MethodLogs, Params: mustJSON(LogsWritten{Namespace: p.Namespace, Application: p.Application, Lines: lines})})
		})
	}
	return nil
}
