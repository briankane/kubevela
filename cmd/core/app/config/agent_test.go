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
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	"github.com/oam-dev/kubevela/pkg/features"
)

func TestAgentFlags(t *testing.T) {
	c := NewAgentConfig()
	require.False(t, c.AgentOnly)
	require.Equal(t, "local", c.ClusterName)

	fs := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	c.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{"--agent-only", "--agent-cluster-name=prod-use1", "--component-producers=hub-user,group:vela-hubs"}))
	require.True(t, c.AgentOnly)
	require.Equal(t, "prod-use1", c.ClusterName)
	require.Equal(t, []string{"hub-user", "group:vela-hubs"}, c.Producers)
}

func TestAgentOnlyNeedsTheGate(t *testing.T) {
	c := &AgentConfig{AgentOnly: true, ClusterName: "prod-use1"}
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, false)
	require.ErrorContains(t, c.Validate(), "EnableComponentDispatch")

	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.EnableComponentDispatch, true)
	require.NoError(t, c.Validate())

	c.ClusterName = ""
	require.ErrorContains(t, c.Validate(), "--agent-cluster-name")
}

func TestSigningKeyMaxAge(t *testing.T) {
	c := NewAgentConfig()
	require.Zero(t, c.SigningKeyMaxAge, "keys rotate only when asked, by default")

	fs := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	c.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{"--component-signing-key-max-age=2160h"}))
	require.Equal(t, 90*24*time.Hour, c.SigningKeyMaxAge)
	require.NoError(t, c.Validate())

	c.SigningKeyMaxAge = -time.Hour
	require.ErrorContains(t, c.Validate(), "--component-signing-key-max-age")
}
