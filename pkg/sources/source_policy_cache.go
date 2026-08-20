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
	"k8s.io/utils/lru"
)

// policyCacheSize bounds the number of distinct (template, properties, context)
// combinations held. Entries are keyed by the CUE source that produced them, so
// the population is the number of distinct binding-and-render combinations in
// flight, not the number of Applications.
const policyCacheSize = 512

// policyCache holds resolved cache policies against the CUE source text that
// produced them.
//
// resolveCachePolicy is the single most expensive thing a render does per
// source, and it runs on every render whether or not the source itself is
// cached. It has to: the policy is what says which stored entry to look for, so
// it is derived before the store is consulted. Measured on one already-cached
// source:
//
//	one cached source, end to end   490,826 ns
//	of which resolveCachePolicy     468,842 ns   (95.5%)
//	a bare compile of the template   62,334 ns
//
// The gap between the last two is evaluation, not parsing: looking up and
// decoding the storage block forces CUE to evaluate what it had left lazy.
//
// The policy is a pure function of the source text assembled in
// resolveCachePolicy - the template, the binding's properties, and the context
// this surface makes readable. Nothing else reaches it, and provider functions
// are explicitly disabled, so there is no I/O and no clock. Keying on that
// exact text is therefore not an approximation of the inputs; it is the inputs.
//
// Keyed on the text rather than a hash of it. A hash would be smaller but
// introduces a collision to reason about, and a collision here would send a
// lookup to another binding's cache entry - a wrong answer rather than a slow
// one. The text is a few kilobytes and Go hashes it on the way into the map.
//
// lru.Cache locks internally, so this carries no mutex.
var policyCache = lru.New(policyCacheSize)

// lookupCachedPolicy returns a previously resolved policy for this source text.
//
// The returned policy owns its KeyInputs, so a caller that keeps or sorts it
// cannot reach back into the cache. Key is a string and TTL a duration, so
// those copy with the struct.
func lookupCachedPolicy(src string) (sourceCachePolicy, bool) {
	hit, ok := policyCache.Get(src)
	if !ok {
		return sourceCachePolicy{}, false
	}
	policy, ok := hit.(sourceCachePolicy)
	if !ok {
		return sourceCachePolicy{}, false
	}
	policy.KeyInputs = append([]string(nil), policy.KeyInputs...)
	return policy, true
}

// storeCachedPolicy records a resolved policy. Only successful resolutions are
// stored: an error is the caller's to report with its own context, and caching
// one would outlive the condition that caused it.
func storeCachedPolicy(src string, policy sourceCachePolicy) {
	policy.KeyInputs = append([]string(nil), policy.KeyInputs...)
	policyCache.Add(src, policy)
}
