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

	appsv1 "k8s.io/api/apps/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

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

// MethodControllerGates is the client's notification of the gates a
// controller image accepts, with their defaults, as its --help listed them.
// The server reads the controller again on it, as it follows a change to
// the controller's gates.
const MethodControllerGates = "vela/controllerGates"

// ControllerGatesParams are an image's gates and their defaults.
type ControllerGatesParams struct {
	Image string           `json:"image"`
	Gates []ControllerGate `json:"gates"`
}

// ControllerGate is a gate as a controller's --help lists it.
type ControllerGate struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// ControllerInfo is the KubeVela controller as its Deployment runs it.
type ControllerInfo struct {
	Image    string
	Version  string
	Replicas int32
	// Set are the gates its --feature-gates args set.
	Set map[string]bool
}

// controllerSelector is the label the chart gives the vela-core Deployment.
var controllerSelector = ctrlclient.MatchingLabels{"controller.oam.dev/name": "vela-core"}

const gateArg = "--feature-gates="

// readController reads the vela-core Deployment; none when there is none.
func readController(ctx context.Context, cli ctrlclient.Client) (*ControllerInfo, error) {
	var list appsv1.DeploymentList
	if err := cli.List(ctx, &list, controllerSelector); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Namespace+"/"+list.Items[i].Name < list.Items[j].Namespace+"/"+list.Items[j].Name
	})
	d := list.Items[0]
	out := &ControllerInfo{Replicas: 1, Set: map[string]bool{}}
	if d.Spec.Replicas != nil {
		out.Replicas = *d.Spec.Replicas
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		gated := false
		for _, arg := range c.Args {
			if !strings.HasPrefix(arg, gateArg) {
				continue
			}
			gated = true
			for _, pair := range strings.Split(strings.TrimPrefix(arg, gateArg), ",") {
				name, value, ok := strings.Cut(pair, "=")
				if ok && strings.TrimSpace(name) != "" {
					out.Set[strings.TrimSpace(name)] = strings.TrimSpace(value) == "true"
				}
			}
		}
		if gated || out.Image == "" {
			out.Image = c.Image
		}
		if gated {
			break
		}
	}
	if i := strings.LastIndex(out.Image, ":"); i >= 0 && !strings.Contains(out.Image[i:], "/") {
		out.Version = strings.SplitN(out.Image[i+1:], "@", 2)[0]
	}
	return out, nil
}

// controllerGates are the gates files are checked against: the args'
// values, else the defaults help gave for the controller's image. A gate
// this server knows that help does not list is one the controller lacks.
// A controller scaled to 0 runs elsewhere, with args of its own, so none.
func controllerGates(context string, ctl *ControllerInfo, help map[string]bool) *analysis.ControllerGates {
	if ctl == nil || ctl.Replicas == 0 {
		return nil
	}
	g := &analysis.ControllerGates{Context: context, Version: ctl.Version, On: map[string]bool{}, Missing: map[string]bool{}}
	for name, def := range help {
		g.On[name] = def
	}
	for name, v := range ctl.Set {
		g.On[name] = v
	}
	if help != nil {
		for _, known := range featureGateDocs {
			if _, listed := help[known.Name]; !listed {
				g.Missing[known.Name] = true
			}
		}
	}
	return g
}

// gates are the controller's gates files are checked against, when the
// cluster is read.
func (s *Server) gates(clusterRead bool) *analysis.ControllerGates {
	if !clusterRead || s.cluster.controller == nil {
		return nil
	}
	return controllerGates(s.cluster.context, s.cluster.controller, s.controllerHelp[s.cluster.controller.Image])
}

// controllerGatesRead keeps an image's gates, reads the controller again off
// the message loop, and checks the open documents against what it found.
func (s *Server) controllerGatesRead(p ControllerGatesParams) {
	if s.controllerHelp == nil {
		s.controllerHelp = map[string]map[string]bool{}
	}
	help := map[string]bool{}
	for _, g := range p.Gates {
		help[g.Name] = g.Default
	}
	s.controllerHelp[p.Image] = help
	cluster := s.cluster
	if cluster == nil || cluster.readController == nil {
		s.republish()
		return
	}
	go func() {
		ctl, err := cluster.readController()
		s.post(func() {
			if err == nil && s.cluster == cluster {
				cluster.controller = ctl
			}
			s.republish()
		})
	}()
}
