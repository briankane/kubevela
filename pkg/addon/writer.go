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

package addon

import (
	"fmt"
)

// AsyncWriter writes a single file back to a registry.
//
// It is the write counterpart to AsyncReader, and deliberately much narrower:
// one file, one commit. The git-backed readers implement it, because each
// already holds the authenticated client a write needs. Object stores and helm
// repos do not, and BuildWriter refuses them by name rather than letting the
// call fail somewhere less obvious.
type AsyncWriter interface {
	// WriteFile writes content at path, which is relative to the registry's own
	// root path - the same relative path ReadFile accepts.
	//
	// It returns the sha of the resulting commit, and whether anything changed.
	// A file that already holds exactly this content is left alone: changed is
	// false and commit is empty. Workflow steps re-evaluate on every reconcile
	// until they complete, and failure, suspend and resume all re-enter them, so
	// a write that committed unconditionally would produce a run of identical
	// commits for a single intent.
	WriteFile(path, content, message string) (commit string, changed bool, err error)
}

// sourceKind names the kind of source a registry is configured with, in
// BuildReader's own precedence order.
//
// The order matters: a registry carrying two sources is read from the first one
// BuildReader would pick, so that is the one an error should name. Returns ""
// for a registry with no source at all.
func (r *Registry) sourceKind() string {
	switch {
	case r.OSS != nil:
		return string(ossType)
	case r.Git != nil:
		return string(gitType)
	case r.Gitee != nil:
		return string(giteeType)
	case r.Gitlab != nil:
		return string(gitlabType)
	case r.Helm != nil:
		return helmKind
	}
	return ""
}

// helmKind names a helm registry in messages. It is not a ReaderType: no reader
// is built from it, since a chart repo is served through the versioned registry
// path instead.
const helmKind = "helm"

// BuildWriter builds an AsyncWriter from a registry, for the git-backed
// registries that support one.
//
// Options are shared with BuildReader, so WithRef selects the branch a write
// commits on, the same way it selects the revision a read resolves at.
//
// Writes use whatever credential the registry was registered with. That is the
// same separation reads rest on: repository credentials stay a platform
// concern, and a caller names a registry rather than carrying a token.
func (r *Registry) BuildWriter(opts ...ReaderOption) (AsyncWriter, error) {
	kind := r.sourceKind()
	if kind == "" {
		return nil, fmt.Errorf("registry %q has no source to write to", r.Name)
	}
	if kind == string(ossType) || kind == helmKind {
		return nil, fmt.Errorf("registry %q is of type %s, which does not support writes", r.Name, kind)
	}

	reader, err := r.BuildReader(opts...)
	if err != nil {
		return nil, err
	}
	writer, ok := reader.(AsyncWriter)
	if !ok {
		return nil, fmt.Errorf("registry %q is of type %s, which does not support writes", r.Name, kind)
	}
	return writer, nil
}
