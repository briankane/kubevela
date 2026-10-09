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

package preview

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTestCases(t *testing.T) {
	cases, err := TestCases(context.Background(), Request{Path: "/defs/web.cue", Source: []byte(provenanceDef), Values: []byte(provenanceValues)}, "renders the shop")
	require.NoError(t, err)
	assert.Contains(t, cases, `"renders the shop": test.#ComponentRender & {`)
	assert.Contains(t, cases, `definition: "web"`)
	assert.Contains(t, cases, "\tdefinition: \"web\"\n\tcontext: {\n\t\tappName:   \"my-app\"\n\t\tname:      \"web\"\n\t\tnamespace: \"default\"\n\t}\n\tparameter:", "the preview's context, which the runner's defaults differ from, after the definition")
	assert.Contains(t, cases, "parameter: {\n\t\timage: \"nginx\"\n\t\tteam:  \"shop\"\n\t}")
	assert.Contains(t, cases, "replicas: 1", "an int stays an int")
	assert.Contains(t, cases, "containerPort: 80")
	assert.Contains(t, cases, "service: {")

	t.Run("a values file of several inputs makes a case each", func(t *testing.T) {
		cases, err := TestCases(context.Background(), Request{Path: "/defs/web.cue", Source: []byte(provenanceDef), Values: []byte("name: small\n" + provenanceValues + "---\nname: big\n" + provenanceValues + "  port: 9090\n")}, "renders")
		require.NoError(t, err)
		assert.Contains(t, cases, `"renders (small)": test.#ComponentRender`)
		assert.Contains(t, cases, `"renders (big)": test.#ComponentRender`)
		assert.Contains(t, cases, "containerPort: 9090")
	})

	t.Run("a trait expects the patched workload", func(t *testing.T) {
		cases, err := TestCases(context.Background(), Request{Path: "/defs/scaler.cue", Source: []byte(provenanceTrait), Values: []byte("parameter:\n  replicas: 3\n")}, "scales")
		require.NoError(t, err)
		assert.Contains(t, cases, `"scales": test.#TraitRender & {`)
		assert.Contains(t, cases, "workload: {")
		assert.Contains(t, cases, "expect: {\n\t\toutput: {")
		assert.Contains(t, cases, "replicas: 3")
	})

	_, err = TestCases(context.Background(), Request{Path: "/defs/web.cue", Source: []byte(provenanceDef)}, "x")
	assert.ErrorContains(t, err, "does not render", "no values: the required image is missing")
}
