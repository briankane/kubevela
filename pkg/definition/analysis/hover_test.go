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

const hoverDoc = `import "vela/http"

"c": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_req: http.#Do & {$params: {method: "GET", url: parameter.url}}
	output: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		metadata: name: context.appName
		data: code: "\(_req.$returns.statusCode)"
	}
	parameter: {
		// +usage=The URL to check
		url: string
		// +usage=How many tries
		tries: *3 | int
	}
}
`

// hoverAt hovers over the middle of word, where it first appears in phrase,
// where phrase first appears in hoverDoc.
func hoverAt(t *testing.T, phrase, word string) string {
	t.Helper()
	i := strings.Index(hoverDoc, phrase)
	require.GreaterOrEqual(t, i, 0, phrase)
	i += strings.Index(phrase, word)
	text, ok := Hover(hoverDoc, i+len(word)/2, Options{})
	require.True(t, ok, "no hover for %s in %s", word, phrase)
	return text
}

func TestHover(t *testing.T) {
	t.Run("a context field", func(t *testing.T) {
		h := hoverAt(t, "context.appName", "appName")
		assert.Contains(t, h, "appName: string")
		assert.Contains(t, h, "The Application's name")
	})
	t.Run("a function", func(t *testing.T) {
		h := hoverAt(t, "http.#Do", "#Do")
		assert.Contains(t, h, "$params")
	})
	t.Run("what a function returns", func(t *testing.T) {
		assert.Contains(t, hoverAt(t, "$returns.statusCode", "statusCode"), "statusCode: int")
	})
	t.Run("a parameter where it is read", func(t *testing.T) {
		h := hoverAt(t, "parameter.url", "url")
		assert.Contains(t, h, "url: string")
		assert.Contains(t, h, "The URL to check")
	})
	t.Run("a field where it is declared", func(t *testing.T) {
		h := hoverAt(t, "tries: *3", "tries")
		assert.Contains(t, h, "tries: int")
		assert.Contains(t, h, "default 3")
		assert.Contains(t, h, "How many tries")
	})
	t.Run("a marker", func(t *testing.T) {
		assert.Contains(t, hoverAt(t, "+usage=The URL", "usage"), "Describes the parameter")
	})
	t.Run("nothing on punctuation", func(t *testing.T) {
		_, ok := Hover(hoverDoc, strings.Index(hoverDoc, "{"), Options{})
		assert.False(t, ok)
	})
}
