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

package sources

import (
	"sync"

	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

// prefetchConcurrency bounds how many bindings resolve at once.
//
// Resolution is waiting on someone else's service, not on this process, so the
// useful width is set by how many outstanding requests are reasonable rather
// than by cores. Small enough not to look like a thundering herd to a registry
// that several bindings happen to share.
const prefetchConcurrency = 8

// prefetch resolves, concurrently, the bindings this render will need that do
// not themselves read another binding.
//
// Resolution is otherwise strictly sequential: each binding is resolved on
// demand as the walker reaches an expression that reads it, so an Application
// reading three registries pays all three round trips end to end, at admission
// and again at every render that misses the cache. Nothing about those three
// reads depends on each other.
//
// Only bindings whose own properties contain no expression are prefetched.
// Those are the leaves of the dependency graph, so they can be resolved in any
// order, and they are also the ones that actually go out to the network - a
// chained binding is usually assembling values that are already in hand. It
// keeps the concurrent phase free of ordering concerns entirely, rather than
// making the resolver safe to share.
//
// Each binding resolves in its own resolver, so nothing mutable is shared while
// the goroutines run; results are merged afterwards. In particular this avoids
// the resolver's readerKind/readerName, which name whoever is currently reading
// and are a call-stack notion that concurrent resolution has no way to express.
//
// This is an optimisation and is never allowed to change an outcome. A binding
// that fails here is left out, and the ordinary lazy path resolves it again and
// reports the failure in its proper place with its proper context. The cost of
// that is a repeated request on a path that was failing anyway.
func (r *sourceResolver) prefetch(properties interface{}) {
	names := r.independentBindings(properties)
	if len(names) < 2 {
		// One binding gains nothing from a goroutine, and zero gains less.
		return
	}

	type result struct {
		name     string
		values   map[string]interface{}
		statuses map[string]SourceResolutionStatus
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		out  = make([]result, 0, len(names))
		gate = make(chan struct{}, prefetchConcurrency)
	)
	for _, name := range names {
		wg.Add(1)
		go func(binding string) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()

			sub := newSourceResolver(r.goCtx, r.ctxValues, r.surface, r.inputs())
			values, err := sub.resolve(binding)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, result{name: binding, values: values, statuses: sub.statuses})
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	for _, res := range out {
		r.resolved[res.name] = res.values
		r.statuses = mergeStatuses(r.statuses, res.statuses)
	}
}

// inputs reconstructs the resolver's inputs, so a sub-resolver reads exactly
// what this one reads.
func (r *sourceResolver) inputs() sourceInputs {
	return sourceInputs{
		Bindings:  r.sourceProps,
		Types:     r.sourceTypes,
		Templates: r.sourceTemplates,
		Sensitive: r.sensitivePaths,
		Store:     r.cacheStore,
		Compiler:  r.compiler,
	}
}

// independentBindings returns the bindings reachable from properties whose own
// properties read nothing, in the order first encountered.
//
// The walk is transitive: a chained binding is not prefetched itself, but the
// bindings it reads are reached through its properties and prefetched, which is
// where the waiting actually happens.
func (r *sourceResolver) independentBindings(properties interface{}) []string {
	seen := map[string]bool{}
	var order []string

	queue := []interface{}{properties}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		//nolint:errcheck // a malformed expression is the lazy path's to report
		_ = walkStrings(node, func(raw string) error {
			parsed, err := propexpr.Parse(raw)
			if err != nil || !parsed.HasExpr() {
				return nil
			}
			for _, fragment := range parsed.Fragments {
				if !fragment.IsExpr() {
					continue
				}
				refs, rerr := expressionReferences(fragment.Expr)
				if rerr != nil {
					continue
				}
				for _, ref := range refs {
					if ref.Root != "source" || len(ref.Path) == 0 {
						continue
					}
					name := ref.Path[0]
					if seen[name] {
						continue
					}
					seen[name] = true

					props, ok := r.sourceProps[name]
					if ok && props != nil && propexpr.HasExpression(props) {
						// Chained: it reads something else, so it is resolved on
						// demand. Its own reads are followed, since those are the
						// ones worth overlapping.
						queue = append(queue, props)
						continue
					}
					order = append(order, name)
				}
			}
			return nil
		})
	}
	return order
}
