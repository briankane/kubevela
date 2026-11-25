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

package multicluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

func TestAClusterReaderReadsTheClusterItsContextNames(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/components/web"):
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"apiVersion":"core.oam.dev/v1alpha1","kind":"Component","metadata":{"name":"web","namespace":"shop"}}`))
		case strings.HasSuffix(r.URL.Path, "/apis/core.oam.dev/v1alpha1"):
			_, _ = w.Write([]byte(`{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"core.oam.dev/v1alpha1","resources":[{"name":"components","singularName":"component","namespaced":true,"kind":"Component","verbs":["get"]}]}`))
		case strings.HasSuffix(r.URL.Path, "/apis"):
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"core.oam.dev","versions":[{"groupVersion":"core.oam.dev/v1alpha1","version":"v1alpha1"}],"preferredVersion":{"groupVersion":"core.oam.dev/v1alpha1","version":"v1alpha1"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/api"):
			_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
		}
	}))
	defer srv.Close()

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(v1alpha1.SchemeGroupVersion.WithKind(v1alpha1.ComponentKind), meta.RESTScopeNamespace)
	reader, err := NewClusterReader(&rest.Config{Host: srv.URL}, mapper)
	require.NoError(t, err)

	key := client.ObjectKey{Namespace: "shop", Name: "web"}
	require.NoError(t, reader.Get(ContextWithClusterName(context.Background(), "spoke"), key, &v1alpha1.Component{}))
	require.NoError(t, reader.Get(ContextWithClusterName(context.Background(), ClusterLocalName), key, &v1alpha1.Component{}))

	require.Equal(t, []string{
		"/apis/cluster.core.oam.dev/v1alpha1/clustergateways/spoke/proxy/apis/core.oam.dev/v1alpha1/namespaces/shop/components/web",
		"/apis/core.oam.dev/v1alpha1/namespaces/shop/components/web",
	}, paths)
}
