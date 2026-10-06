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

package kubeschema

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// builtinFS is the OpenAPI v3 documents of Kubernetes' own API groups, as
// the kube-apiserver of the Kubernetes version KubeVela builds against serves
// them. TestBuiltinDocuments captures them.
//
//go:embed builtin/*.json.gz
var builtinFS embed.FS

// builtinResources are the built-in kinds a trait's appliesToWorkloads names
// by resource, which an OpenAPI document does not say.
var builtinResources = map[string]GVK{
	"deployments.apps":            {Group: "apps", Version: "v1", Kind: "Deployment"},
	"statefulsets.apps":           {Group: "apps", Version: "v1", Kind: "StatefulSet"},
	"daemonsets.apps":             {Group: "apps", Version: "v1", Kind: "DaemonSet"},
	"replicasets.apps":            {Group: "apps", Version: "v1", Kind: "ReplicaSet"},
	"jobs.batch":                  {Group: "batch", Version: "v1", Kind: "Job"},
	"cronjobs.batch":              {Group: "batch", Version: "v1", Kind: "CronJob"},
	"pods":                        {Version: "v1", Kind: "Pod"},
	"services":                    {Version: "v1", Kind: "Service"},
	"configmaps":                  {Version: "v1", Kind: "ConfigMap"},
	"secrets":                     {Version: "v1", Kind: "Secret"},
	"ingresses.networking.k8s.io": {Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
}

// Builtin returns a set holding Kubernetes' own kinds.
func Builtin() (*Schemas, error) {
	s := New()
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json.gz") {
			continue
		}
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		doc, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := s.AddDocument(doc); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	for resource, gvk := range builtinResources {
		s.resources[resource] = gvk
	}
	return s, nil
}
