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

// MethodFeatureGates lists the feature gates KubeVela, its workflow engine
// and cluster-gateway declare, so a client can tell them from the
// Kubernetes libraries' among the gates a controller binary accepts.
const MethodFeatureGates = "vela/featureGates"

// FeatureGateInfo is one gate: its name, the module declaring it (kubevela,
// workflow or multicluster) and its doc comment.
type FeatureGateInfo struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

// FeatureGatesResult is every gate this server's KubeVela is built with,
// by name.
type FeatureGatesResult struct {
	Gates []FeatureGateInfo `json:"gates"`
}
