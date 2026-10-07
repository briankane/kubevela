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
)

// at splits a document at the cursor, marked |.
func at(doc string) (string, int) {
	i := strings.Index(doc, "|")
	return doc[:i] + doc[i+1:], i
}

const callsFunctions = `import (
	"vela/http"
	"vela/kube"
)

"c": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_req: http.#Do & {$params: {method: "GET", url: "https://example.com"}}
	_dep: kube.#Get & {$params: resource: {apiVersion: "apps/v1", kind: "Deployment", metadata: name: "web"}}
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		data: {
			code: CURSOR
		}
	}
}
`

func TestCompleteFunctionResults(t *testing.T) {
	complete := func(expr string) []string {
		doc, cursor := at(strings.Replace(callsFunctions, "CURSOR", expr+"|", 1))
		return labels(CompleteValueAt(doc, cursor, nil))
	}
	t.Run("what a function declares it returns", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"body", "header", "statusCode", "trailer"}, complete("_req.$returns."))
	})
	t.Run("narrowed by what is typed", func(t *testing.T) {
		assert.Equal(t, []string{"statusCode"}, complete("_req.$returns.st"))
	})
	t.Run("the fields of the call", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"$params", "$returns"}, complete("_req."))
	})
	t.Run("a resource read, from its kind's schema", func(t *testing.T) {
		got := complete("_dep.$returns.")
		assert.Contains(t, got, "spec")
		assert.Contains(t, got, "status")
		assert.Contains(t, complete("_dep.$returns.spec."), "replicas")
	})
	t.Run("nothing for a name the template does not declare", func(t *testing.T) {
		assert.Empty(t, complete("_nope."))
	})
	t.Run("in the middle of a word already written", func(t *testing.T) {
		doc, cursor := at(strings.Replace(callsFunctions, "CURSOR", "_req.$returns.|statusCode", 1))
		assert.Contains(t, labels(CompleteValueAt(doc, cursor, nil)), "statusCode")
	})
}

// A name is found the way CUE finds it: in the nearest enclosing struct that
// declares it.
func TestCompleteFunctionResultsInABlock(t *testing.T) {
	src := `import "vela/kube"

"c": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		_read: kube.#Get & {$params: resource: {apiVersion: "apps/v1", kind: "Deployment", metadata: name: "web"}}
		data: replicas: "\(_read.$returns.spec.|)"
	}
}
`
	doc, cursor := at(src)
	assert.Contains(t, labels(CompleteValueAt(doc, cursor, nil)), "replicas")
}

// What a call reads, with no kind given yet, is offered as any Kubernetes
// object: its apiVersion, kind, metadata (with its fields), spec and status.
func TestCompleteReturnsOfAnUnknownKind(t *testing.T) {
	doc := "import \"vela/kube\"\n\n\"web\": {\n\ttype: \"component\"\n\tattributes: workload: type: \"autodetects.core.oam.dev\"\n}\ntemplate: {\n\tvalue: kube.#Get & {\n\t\t$params: resource: {apiVersion: \"\", kind: \"\", metadata: name: \"\"}\n\t}\n\tx: CURSOR\n\toutput: {apiVersion: \"v1\", kind: \"ConfigMap\"}\n\tparameter: {}\n}\n"
	labels := func(typed string) []string {
		src := strings.Replace(doc, "CURSOR", typed, 1)
		var out []string
		for _, c := range CompleteValueAt(src, strings.Index(src, typed)+len(typed), nil) {
			out = append(out, c.Label)
		}
		return out
	}
	assert.Subset(t, labels("value.$returns."), []string{"apiVersion", "kind", "metadata", "spec", "status"})
	assert.Subset(t, labels("value.$returns.metadata."), []string{"name", "namespace", "labels"})
}
