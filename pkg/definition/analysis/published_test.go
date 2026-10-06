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

package analysis

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const globalPolicy = `"platform-defaults": {
	type: "policy"
	attributes: {
		scope:  "Application"
		global: true
	}
}
template: {
	output: {
		labels: team: "platform"
		ctx: {
			region:   parameter.region
			tier:     "gold"
			replicas: 3
			sidecar: {enabled: true, image: "envoy"}
		}
	}
	parameter: region: *"eu-west-1" | string
}
`

func TestPublishedContext(t *testing.T) {
	p, ok := PublishedContext("policy.cue", []byte(globalPolicy))
	require.True(t, ok)
	assert.Equal(t, "platform-defaults", p.Policy)
	byName := map[string]ContextField{}
	for _, f := range p.Fields {
		byName[f.Name] = f
	}
	require.Len(t, byName, 4)
	assert.Equal(t, "string", byName["tier"].Type)
	assert.Equal(t, "int", byName["replicas"].Type)
	assert.Equal(t, "string", byName["region"].Type, "read from its parameter")
	assert.Equal(t, "{enabled: bool, image: string}", byName["sidecar"].Type)
	assert.Contains(t, byName["tier"].Doc, "platform-defaults")
}

func TestPublishedContextIsOnlyAGlobalApplicationPolicy(t *testing.T) {
	for name, src := range map[string]string{
		"not global":       globalPolicyWith("global: false"),
		"not app-scoped":   globalPolicyWith(`scope: "Default"`),
		"not a policy":     componentHeader + "template: output: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n",
		"publishes no ctx": "\"p\": {\n\ttype: \"policy\"\n\tattributes: {scope: \"Application\", global: true}\n}\ntemplate: output: labels: a: \"b\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := PublishedContext("x.cue", []byte(src))
			assert.False(t, ok)
		})
	}
}

func globalPolicyWith(change string) string {
	src := globalPolicy
	switch change {
	case "global: false":
		return replaceOnce(src, "global: true", change)
	default:
		return replaceOnce(src, `scope:  "Application"`, change)
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestCompleteCustomContext(t *testing.T) {
	p, _ := PublishedContext("policy.cue", []byte(globalPolicy))
	component := "\"c\": {\n\ttype: \"component\"\n}\ntemplate: output: metadata: name: "
	cs := CompleteContextWith(component, "context.custom.", []Published{p})
	assert.ElementsMatch(t, []string{"region", "replicas", "sidecar", "tier"}, labels(cs))
	assert.ElementsMatch(t, []string{"enabled", "image"}, labels(CompleteContextWith(component, "context.custom.sidecar.", []Published{p})))
	assert.Empty(t, CompleteContextWith(component, "context.custom.", nil))
}
