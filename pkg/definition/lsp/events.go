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
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	pkgmulticluster "github.com/kubevela/pkg/multicluster"
)

// MethodWatchEvents starts sending an Application's events as they change,
// as MethodEventsChanged notifications, until MethodUnwatchEvents. It is this
// server's own request.
const MethodWatchEvents = "vela/watchEvents"

// MethodUnwatchEvents ends a watch MethodWatchEvents started.
const MethodUnwatchEvents = "vela/unwatchEvents"

// MethodEventsChanged is the server's notification of an Application's
// events, newest first.
const MethodEventsChanged = "vela/eventsChanged"

// WatchEventsParams name the Application, and the names of what it applied,
// whose events, and those of what they made, are its.
type WatchEventsParams struct {
	Namespace   string   `json:"namespace"`
	Application string   `json:"application"`
	Names       []string `json:"names,omitempty"`
	// Clusters are the member clusters it applied resources on, whose events
	// are watched beside the hub's.
	Clusters []string `json:"clusters,omitempty"`
}

// EventsChanged are an Application's events, newest first.
type EventsChanged struct {
	Namespace   string      `json:"namespace"`
	Application string      `json:"application"`
	Events      []EventView `json:"events"`
}

// EventView is one event: what it is about, as Kind/name, and what it says.
type EventView struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Object  string `json:"object"`
	Count   int64  `json:"count"`
	Last    string `json:"last"`
	// Cluster is the member cluster it happened on; none for the hub.
	Cluster string `json:"cluster,omitempty"`
}

// maxEvents is the most events sent at once: the newest.
const maxEvents = 200

var eventsResource = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// eventMatches reports whether an event is about one of names, or about
// what one made and named after itself, as a Deployment's pods are.
func eventMatches(ev *unstructured.Unstructured, names []string) bool {
	obj, _, _ := unstructured.NestedString(ev.Object, "involvedObject", "name")
	for _, n := range names {
		if obj == n || strings.HasPrefix(obj, n+"-") && looksGenerated(strings.TrimPrefix(obj, n+"-")) {
			return true
		}
	}
	return false
}

// looksGenerated reports whether a name's suffix is one Kubernetes generates
// for what a resource makes: an ordinal, a hash, or five random characters;
// a word, as another resource's name has, is not.
func looksGenerated(suffix string) bool {
	for _, part := range strings.Split(suffix, "-") {
		if part == "" || len(part) > 10 {
			return false
		}
		digits := strings.IndexFunc(part, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0
		if !digits && len(part) != 5 {
			return false
		}
	}
	return true
}

// eventTime is when an event last happened.
func eventTime(ev *unstructured.Unstructured) string {
	for _, path := range [][]string{{"lastTimestamp"}, {"eventTime"}, {"metadata", "creationTimestamp"}} {
		if t, _, _ := unstructured.NestedString(ev.Object, path...); t != "" {
			return t
		}
	}
	return ""
}

// eventsOf are the events about names, newest first, at most maxEvents.
func eventsOf(all []*unstructured.Unstructured, names []string) []EventView {
	out := []EventView{}
	for _, ev := range all {
		if !eventMatches(ev, names) {
			continue
		}
		str := func(path ...string) string { s, _, _ := unstructured.NestedString(ev.Object, path...); return s }
		count, _, _ := unstructured.NestedInt64(ev.Object, "count")
		out = append(out, EventView{
			Type:    str("type"),
			Reason:  str("reason"),
			Message: str("message"),
			Object:  str("involvedObject", "kind") + "/" + str("involvedObject", "name"),
			Count:   count,
			Last:    eventTime(ev),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out
}

// watchEvents calls each with every event in a namespace, then again as
// they change, until ctx is done. A watch the API server ends is made again.
func watchEvents(ctx context.Context, cfg *rest.Config, namespace string, each func([]*unstructured.Unstructured)) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return
	}
	events := client.Resource(eventsResource).Namespace(namespace)
	for ctx.Err() == nil {
		list, err := events.List(ctx, metav1.ListOptions{})
		if err == nil {
			byUID := map[string]*unstructured.Unstructured{}
			for i := range list.Items {
				byUID[string(list.Items[i].GetUID())] = &list.Items[i]
			}
			send := func() {
				all := make([]*unstructured.Unstructured, 0, len(byUID))
				for _, ev := range byUID {
					all = append(all, ev)
				}
				each(all)
			}
			send()
			if w, err := events.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion()}); err == nil {
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

// eventsKey is the key of an Application's events watch.
func eventsKey(namespace, app string) string {
	return "events:" + namespace + "/" + app
}

// startEventsWatch answers MethodWatchEvents: it ends any watch of the same
// Application's events, then watches its namespace's off the message loop.
func (s *Server) startEventsWatch(p WatchEventsParams) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.events == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	key := eventsKey(p.Namespace, p.Application)
	s.stopKey(key)
	ctx, cancel := context.WithCancel(context.Background())
	if s.watches == nil {
		s.watches = map[string]context.CancelFunc{}
	}
	s.watches[key] = cancel
	names := append([]string{p.Application}, p.Names...)
	watchFn := s.cluster.events
	var mu sync.Mutex
	latest := map[string][]EventView{}
	for _, cluster := range append([]string{pkgmulticluster.Local}, p.Clusters...) {
		go watchFn(ctx, cluster, p.Namespace, func(all []*unstructured.Unstructured) {
			if ctx.Err() != nil {
				return
			}
			views := eventsOf(all, names)
			if cluster != pkgmulticluster.Local {
				for i := range views {
					views[i].Cluster = cluster
				}
			}
			mu.Lock()
			latest[cluster] = views
			merged := mergeEvents(latest)
			mu.Unlock()
			change := EventsChanged{Namespace: p.Namespace, Application: p.Application, Events: merged}
			_ = s.write(message{JSONRPC: "2.0", Method: MethodEventsChanged, Params: mustJSON(change)})
		})
	}
	return nil
}

// mergeEvents are each cluster's events as one list, newest first.
func mergeEvents(byCluster map[string][]EventView) []EventView {
	clusters := make([]string, 0, len(byCluster))
	for c := range byCluster {
		clusters = append(clusters, c)
	}
	sort.Strings(clusters)
	out := []EventView{}
	for _, c := range clusters {
		out = append(out, byCluster[c]...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out
}

// stopKey ends the watch under key, if there is one.
func (s *Server) stopKey(key string) {
	if cancel, ok := s.watches[key]; ok {
		cancel()
		delete(s.watches, key)
	}
}
