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

package config

import (
	"errors"
	"time"

	"github.com/spf13/pflag"
	utilfeature "k8s.io/apiserver/pkg/util/feature"

	"github.com/oam-dev/kubevela/pkg/features"
)

// AgentConfig configures Component dispatch in vela-core: vela-agent, the Component
// reconciler, and the hub's signing keys.
type AgentConfig struct {
	// AgentOnly runs vela-core as vela-agent alone, on a cluster the hub dispatches to.
	AgentOnly bool
	// ClusterName is this cluster's name as the hub addresses it.
	ClusterName string
	// Producers may create Components and change their spec: usernames, and group:<name>.
	Producers []string
	// SigningKeyMaxAge, on the hub, rotates the signing key once it has been active this long;
	// zero rotates only when asked.
	SigningKeyMaxAge time.Duration
}

// NewAgentConfig returns the defaults: the full controller, on the hub ("local").
func NewAgentConfig() *AgentConfig {
	return &AgentConfig{ClusterName: "local"}
}

// AddFlags registers the agent flags.
func (c *AgentConfig) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&c.AgentOnly, "agent-only", c.AgentOnly,
		"Run only vela-agent, which applies the Components a hub dispatches to this cluster. Needs the EnableComponentDispatch feature gate.")
	fs.StringVar(&c.ClusterName, "agent-cluster-name", c.ClusterName,
		"This cluster's name as the hub addresses it. vela-agent refuses Components addressed to any other cluster.")
	fs.StringSliceVar(&c.Producers, "component-producers", c.Producers,
		"Identities allowed to create Components and change their spec: usernames (e.g. system:serviceaccount:vela-system:kubevela-vela-core) and group:<name> entries. On a cluster a hub pushes to, the identity the hub joined it with. With none, every such write is refused.")
	fs.DurationVar(&c.SigningKeyMaxAge, "component-signing-key-max-age", c.SigningKeyMaxAge,
		"On the hub, rotate the Component signing key once it has been active this long. Zero rotates only when the vela-component-signing-keys Secret is annotated with component.oam.dev/rotate-signing-key.")
}

// Validate checks the agent flags against the feature gate.
func (c *AgentConfig) Validate() error {
	if c.AgentOnly && !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableComponentDispatch) {
		return errors.New("--agent-only needs the EnableComponentDispatch feature gate")
	}
	if c.ClusterName == "" {
		return errors.New("--agent-cluster-name cannot be empty")
	}
	if c.SigningKeyMaxAge < 0 {
		return errors.New("--component-signing-key-max-age cannot be negative")
	}
	return nil
}
