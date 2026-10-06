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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callIn(call string) string {
	return "import \"vela/kube\"\n" + componentHeader + "template: {\n\t_r: " + call + "\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: res: {...}\n}\n"
}

// A parameter the function sets itself, as http.#Get sets method, is not
// the caller's to give.
func TestCallsNeedNotGiveWhatTheFunctionSets(t *testing.T) {
	src := "import \"vela/http\"\n" + componentHeader + "template: {\n\t_r: http.#Get & {$params: url: \"https://x\"}\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n}\n"
	assert.Empty(t, lines(withoutInfo(Analyze("x.cue", []byte(src)).Diagnostics)))
}

func TestCallsGiveTheirRequiredParameters(t *testing.T) {
	for name, tc := range map[string]struct{ call, want string }{
		"every required parameter given": {call: `kube.#Get & {$params: resource: {apiVersion: "v1", kind: "ConfigMap", metadata: name: "x"}}`},
		"given by reference":             {call: `kube.#Get & {$params: resource: parameter.res}`},
		"a required parameter left out":  {call: `kube.#Get & {$params: cluster: "local"}`, want: "kube.#Get needs $params.resource"},
		"a nested one left out":          {call: `kube.#Get & {$params: resource: {apiVersion: "v1", kind: "ConfigMap"}}`, want: "kube.#Get needs $params.resource.metadata"},
		"no $params at all":              {call: `kube.#Get`, want: "kube.#Get needs $params.resource"},
	} {
		t.Run(name, func(t *testing.T) {
			got := lines(withoutInfo(Analyze("x.cue", []byte(callIn(tc.call))).Diagnostics))
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1, strings.Join(got, "\n"))
			assert.Contains(t, got[0], "7: "+tc.want)
		})
	}
}
