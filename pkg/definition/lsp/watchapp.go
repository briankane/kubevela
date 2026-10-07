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
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// MethodWatchApplication starts sending an Application as it changes, as
// MethodApplicationChanged notifications, until MethodUnwatchApplication. It
// is this server's own request.
const MethodWatchApplication = "vela/watchApplication"

// MethodUnwatchApplication ends a watch MethodWatchApplication started.
const MethodUnwatchApplication = "vela/unwatchApplication"

// MethodApplicationChanged is the server's notification of an Application
// watched: as it is now, or Gone once it is deleted.
const MethodApplicationChanged = "vela/applicationChanged"

// WatchApplicationParams name the Application.
type WatchApplicationParams struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ApplicationChanged is an Application watched, as it is now.
type ApplicationChanged struct {
	Namespace   string                 `json:"namespace"`
	Name        string                 `json:"name"`
	Application map[string]interface{} `json:"application,omitempty"`
	Gone        bool                   `json:"gone,omitempty"`
}

// watchRetry is how long a watch waits before it is made again, once the
// API server ends it, as it does every few minutes.
const watchRetry = time.Second

// watchApplication calls each with the Application, then with each version
// of it, nil once it is deleted, until ctx is done. A watch the API server
// ends is made again from the version last seen.
func watchApplication(ctx context.Context, cfg *rest.Config, namespace, name string, each func(*unstructured.Unstructured)) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return
	}
	apps := client.Resource(v1beta1.SchemeGroupVersion.WithResource("applications")).Namespace(namespace)
	// version is the one last sent; a watch may send it again as it starts.
	version := ""
	send := func(app *unstructured.Unstructured) {
		if app != nil && app.GetResourceVersion() == version {
			return
		}
		version = ""
		if app != nil {
			version = app.GetResourceVersion()
		}
		each(app)
	}
	for ctx.Err() == nil {
		app, err := apps.Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			send(app)
		case kerrors.IsNotFound(err):
			send(nil)
		}
		if w, err := apps.Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name, ResourceVersion: version}); err == nil {
			for ev := range w.ResultChan() {
				obj, ok := ev.Object.(*unstructured.Unstructured)
				switch {
				case ev.Type == watch.Deleted:
					send(nil)
				case ok && (ev.Type == watch.Added || ev.Type == watch.Modified):
					send(obj)
				}
			}
			w.Stop()
		}
		select {
		case <-ctx.Done():
		case <-time.After(watchRetry):
		}
	}
}

// watchKey is a watch's key: the Application's namespace and name.
func watchKey(p WatchApplicationParams) string {
	return p.Namespace + "/" + p.Name
}

// startWatch answers MethodWatchApplication: it ends any watch of the same
// Application, then watches it off the message loop.
func (s *Server) startWatch(p WatchApplicationParams) *ResponseError {
	switch {
	case !s.clusterEnabled:
		return &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.watch == nil:
		return &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	s.stopWatch(p)
	ctx, cancel := context.WithCancel(context.Background())
	if s.watches == nil {
		s.watches = map[string]context.CancelFunc{}
	}
	s.watches[watchKey(p)] = cancel
	watchFn := s.cluster.watch
	go watchFn(ctx, p.Namespace, p.Name, func(app *unstructured.Unstructured) {
		if ctx.Err() != nil {
			return
		}
		change := ApplicationChanged{Namespace: p.Namespace, Name: p.Name, Gone: app == nil}
		if app != nil {
			change.Application = app.Object
		}
		_ = s.write(message{JSONRPC: "2.0", Method: MethodApplicationChanged, Params: mustJSON(change)})
	})
	return nil
}

// stopWatch ends the watch of an Application, if there is one.
func (s *Server) stopWatch(p WatchApplicationParams) {
	s.stopKey(watchKey(p))
}

// stopWatches ends every watch, as the server shuts down.
func (s *Server) stopWatches() {
	for key, cancel := range s.watches {
		cancel()
		delete(s.watches, key)
	}
}

// watchRequest answers the requests that start and end watches.
func (s *Server) watchRequest(msg message) (interface{}, *ResponseError) {
	switch msg.Method {
	case MethodWatchApplication, MethodUnwatchApplication:
		var p WatchApplicationParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		if msg.Method == MethodUnwatchApplication {
			s.stopWatch(p)
			return struct{}{}, nil
		}
		return struct{}{}, s.startWatch(p)
	case MethodWatchLogs, MethodUnwatchLogs:
		var p WatchLogsParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		if msg.Method == MethodUnwatchLogs {
			s.stopKey(logsKey(p.Namespace, p.Application))
			return struct{}{}, nil
		}
		return struct{}{}, s.startLogsWatch(p)
	default:
		var p WatchEventsParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		if msg.Method == MethodUnwatchEvents {
			s.stopKey(eventsKey(p.Namespace, p.Application))
			return struct{}{}, nil
		}
		return struct{}{}, s.startEventsWatch(p)
	}
}
