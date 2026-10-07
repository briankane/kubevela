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
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/goloader"
)

const goURI = "file:///defs/worker.go"

func renderRequest(c *client) int {
	return c.send(MethodRenderDefKit, RenderDefKitParams{TextDocument: TextDocumentIdentifier{URI: goURI}}, true)
}

func renderResult(t *testing.T, m map[string]json.RawMessage) RenderDefKitResult {
	require.Empty(t, string(m["error"]))
	var r RenderDefKitResult
	require.NoError(t, json.Unmarshal(m["result"], &r))
	return r
}

func TestRenderDefKit(t *testing.T) {
	var asked string
	c := newClientWith(t, NewServer(WithGoRenderer(func(path string) ([]goloader.LoadResult, error) {
		asked = path
		return []goloader.LoadResult{
			{Definition: goloader.DefinitionInfo{Name: "my-worker", Type: "component"}, CUE: fixedDef},
			{Definition: goloader.DefinitionInfo{Name: "broken", Type: "component"}, CUE: brokenDef},
			{Definition: goloader.DefinitionInfo{Name: "failed", Type: "trait"}, Error: errors.New("defkit: no template")},
		}, nil
	})))

	m := c.response(renderRequest(c))
	assert.Equal(t, "/defs/worker.go", asked, "the file is rendered from disk, by path")
	r := renderResult(t, m)
	require.Len(t, r.Definitions, 3)

	ok := r.Definitions[0]
	assert.Equal(t, RenderedDefinition{Name: "my-worker", Type: "component", CUE: fixedDef, Diagnostics: []Diagnostic{}}, ok)

	broken := r.Definitions[1]
	assert.Equal(t, brokenDef, broken.CUE)
	require.Len(t, broken.Diagnostics, 1, "the generated CUE is checked like a hand-written definition")
	assert.Contains(t, broken.Diagnostics[0].Message, "nmae")
	assert.Equal(t, uint32(5), broken.Diagnostics[0].Range.Start.Line, "positions are in that definition's own CUE")

	failed := r.Definitions[2]
	assert.Equal(t, "defkit: no template", failed.Error)
	assert.Empty(t, failed.CUE)
}

func TestRenderDefKitReportsALoadError(t *testing.T) {
	c := newClientWith(t, NewServer(WithGoRenderer(func(string) ([]goloader.LoadResult, error) {
		return nil, errors.New("worker.go:12:2: undefined: defkit.Nope")
	})))
	r := renderResult(t, c.response(renderRequest(c)))
	assert.Equal(t, "worker.go:12:2: undefined: defkit.Nope", r.Error)
	assert.Empty(t, r.Definitions)
}

func TestRenderDefKitSupersedesAnOlderRender(t *testing.T) {
	started := make(chan int32, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	c := newClientWith(t, NewServer(WithGoRenderer(func(string) ([]goloader.LoadResult, error) {
		n := calls.Add(1)
		started <- n
		if n == 1 {
			<-release
		}
		return []goloader.LoadResult{{Definition: goloader.DefinitionInfo{Name: "my-worker", Type: "component"}, CUE: fixedDef}}, nil
	})))

	first := renderRequest(c)
	<-started

	// Diagnostics keep flowing while a render runs.
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: uri, LanguageID: "cue", Version: 1, Text: brokenDef}}, false)
	assert.Len(t, c.diagnostics().Diagnostics, 1)

	second := renderRequest(c)
	<-started
	m := c.response(second)
	assert.Len(t, renderResult(t, m).Definitions, 1)

	close(release)
	m = c.response(first)
	var e ResponseError
	require.NoError(t, json.Unmarshal(m["error"], &e))
	assert.Equal(t, CodeRequestCancelled, e.Code, "a save that was rendered over is not shown")
}

// A rendered definition is checked with what the workspace offers, as a
// hand-written one is: here, the parent it extends.
func TestRenderDefKitChecksWithTheWorkspace(t *testing.T) {
	c := newClientWith(t, NewServer(WithGoRenderer(func(string) ([]goloader.LoadResult, error) {
		return []goloader.LoadResult{{Definition: goloader.DefinitionInfo{Name: "tenant-web", Type: "component"}, CUE: childSrc}}, nil
	})))
	c.drain()
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: "file:///defs/web.cue", LanguageID: "cue", Version: 1, Text: parentSrc}}, false)
	r := renderResult(t, c.response(renderRequest(c)))
	require.Len(t, r.Definitions, 1)
	assert.Contains(t, messages(r.Definitions[0].Diagnostics), "image", "the workspace's web requires image")
}

const renderedRefs = `"hello": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: {apiVersion: "v1", kind: "ConfigMap", data: img: parameter.image}
	parameter: image: string
}
`

// Go to definition in a definition's generated CUE: a target in that CUE
// comes back with no URI, one elsewhere with its own.
func TestRenderedDefinition(t *testing.T) {
	c := newClientWith(t, NewServer())
	c.drain()
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{URI: "file:///defs/web.cue", LanguageID: "cue", Version: 1, Text: parentSrc}}, false)
	ask := func(text string, line, char uint32) []Location {
		m := c.response(c.send(MethodRenderedDefinition, RenderedDefinitionParams{
			TextDocument: TextDocumentIdentifier{URI: goURI}, Name: "hello", Text: text, Position: Position{Line: line, Character: char},
		}, true))
		require.Empty(t, string(m["error"]))
		var locs []Location
		require.NoError(t, json.Unmarshal(m["result"], &locs))
		return locs
	}

	at := strings.Index(strings.Split(renderedRefs, "\n")[5], "image")
	locs := ask(renderedRefs, 5, uint32(at))
	require.Len(t, locs, 1)
	assert.Equal(t, "", locs[0].URI, "parameter.image is declared in the same CUE")
	assert.Equal(t, uint32(6), locs[0].Range.Start.Line)

	child := strings.Replace(childSrc, "$super: properties: imge", "$super: properties: image", 1)
	at = strings.Index(strings.Split(child, "\n")[2], "web")
	locs = ask(child, 2, uint32(at))
	require.Len(t, locs, 1)
	assert.Equal(t, "file:///defs/web.cue", locs[0].URI, "the parent it extends, in the workspace")
}
