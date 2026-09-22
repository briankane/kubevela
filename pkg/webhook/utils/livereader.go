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

package utils

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadThroughCache answers reads from cached and retries live only when the
// cache comes up empty.
//
// Admission runs on the write path, and the Application webhook fires on every
// update a controller makes while reconciling, so a live read per request is
// paid for by the whole write path rather than by the rare stale one. Resolving
// a pinned `@v1` lists DefinitionRevisions across two namespaces, which off the
// cache is free and off the API server is two round-trips.
//
// The miss is the case worth paying for: a parent written milliseconds before
// its child has not reached the cache yet, and reading absence off a stale cache
// refuses a definition that is plainly there.
//
// A nil live client leaves cached unwrapped, so a test supplying a fake gets
// back exactly what it supplied.
func ReadThroughCache(cached, live client.Client) client.Client {
	if live == nil {
		return cached
	}
	return &readThroughCache{Client: cached, live: live}
}

// readThroughCache embeds the cached client, so everything but the two reads it
// overrides behaves as before.
type readThroughCache struct {
	client.Client
	live client.Client
}

func (r *readThroughCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Client.Get(ctx, key, obj, opts...)
	if apierrors.IsNotFound(err) {
		return r.live.Get(ctx, key, obj, opts...)
	}
	return err
}

func (r *readThroughCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if meta.LenList(list) > 0 {
		return nil
	}
	return r.live.List(ctx, list, opts...)
}

// LiveClient returns a client that reads straight from the API server, for use
// behind ReadThroughCache.
//
// A full client rather than mgr.GetAPIReader(): resolving a pinned revision
// asserts its reader back to a client.Client.
//
// Returns nil if a client cannot be built, so a caller can fall back to the
// cached one rather than refuse to start: stale reads are a worse failure than
// no webhook, but neither is worth a crash loop.
func LiveClient(mgr ctrl.Manager) client.Client {
	cli, err := client.New(mgr.GetConfig(), client.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
	})
	if err != nil {
		klog.ErrorS(err, "no uncached client for admission; definition chains will be resolved from the cache "+
			"and may briefly not see a parent written alongside its child")
		return nil
	}
	return cli
}
