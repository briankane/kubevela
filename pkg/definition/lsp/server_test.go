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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const uri = "file:///defs/my-worker.cue"

const brokenDef = `"my-worker": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: metadata: name: context.nmae
	output: {apiVersion: "v1", kind: "ConfigMap"}
}
`

const fixedDef = `"my-worker": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	output: metadata: name: context.name
	output: {apiVersion: "v1", kind: "ConfigMap"}
}
`

// client drives a Server over in-memory pipes.
type client struct {
	t    *testing.T
	in   io.WriteCloser
	out  *bufio.Reader
	next int
	done chan error
	// pending carries messages once tryRead has taken over reading.
	pending chan map[string]json.RawMessage
}

func newClient(t *testing.T) *client {
	return newClientWith(t, NewServer(WithCluster(noCluster)))
}

// newClientWith drives s.
func newClientWith(t *testing.T, s *Server) *client {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	c := &client{t: t, in: cw, out: bufio.NewReader(cr), done: make(chan error, 1)}
	go func() {
		c.done <- s.Serve(context.Background(), sr, sw)
		_ = sw.Close()
	}()
	return c
}

func (c *client) send(method string, params interface{}, isRequest bool) int {
	msg := map[string]interface{}{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	id := 0
	if isRequest {
		c.next++
		id = c.next
		msg["id"] = id
	}
	require.NoError(c.t, writeMessage(c.in, msg))
	return id
}

// read returns the next message the server sends.
func (c *client) read() map[string]json.RawMessage {
	if c.pending != nil {
		m, ok := c.tryRead(10 * time.Second)
		if !ok {
			c.t.Fatal("timed out waiting for the server")
		}
		return m
	}
	type result struct {
		msg map[string]json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		body, err := readMessage(c.out)
		var m map[string]json.RawMessage
		if err == nil {
			err = json.Unmarshal(body, &m)
		}
		ch <- result{m, err}
	}()
	select {
	case r := <-ch:
		require.NoError(c.t, r.err)
		return r.msg
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for the server")
		return nil
	}
}

// tryRead returns the next message the server sends within wait, if any.
// A read it gives up on is abandoned, so it ends the client's use.
func (c *client) tryRead(wait time.Duration) (map[string]json.RawMessage, bool) {
	if c.pending == nil {
		c.pending = make(chan map[string]json.RawMessage, 1)
		go func() {
			for {
				body, err := readMessage(c.out)
				if err != nil {
					close(c.pending)
					return
				}
				var m map[string]json.RawMessage
				if json.Unmarshal(body, &m) == nil {
					c.pending <- m
				}
			}
		}()
	}
	select {
	case m, ok := <-c.pending:
		return m, ok
	case <-time.After(wait):
		return nil, false
	}
}

// response reads the next message, which must answer request id.
// response reads up to the response to request id, past any notifications.
func (c *client) response(id int) map[string]json.RawMessage {
	for {
		m := c.read()
		if _, notification := m["method"]; notification {
			continue
		}
		require.Equal(c.t, fmt.Sprint(id), string(m["id"]))
		return m
	}
}

func (c *client) diagnostics() PublishDiagnosticsParams {
	m := c.read()
	// The cluster's status is told the client as it changes, between other
	// messages.
	for string(m["method"]) == `"`+MethodClusterStatus+`"` {
		m = c.read()
	}
	require.Equal(c.t, `"textDocument/publishDiagnostics"`, string(m["method"]))
	var p PublishDiagnosticsParams
	require.NoError(c.t, json.Unmarshal(m["params"], &p))
	return p
}

func TestServerLifecycle(t *testing.T) {
	c := newClient(t)

	id := c.send("initialize", map[string]interface{}{"processId": nil, "capabilities": map[string]interface{}{}}, true)
	m := c.read()
	assert.Equal(t, `1`, string(m["id"]))
	assert.Equal(t, 1, id)
	var init InitializeResult
	require.NoError(t, json.Unmarshal(m["result"], &init))
	assert.Equal(t, TextDocumentSyncFull, init.Capabilities.TextDocumentSync.Change)
	assert.True(t, init.Capabilities.TextDocumentSync.OpenClose)
	c.send("initialized", map[string]interface{}{}, false)

	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: uri, LanguageID: "cue", Version: 1, Text: brokenDef,
	}}, false)
	p := c.diagnostics()
	assert.Equal(t, uri, p.URI)
	require.Len(t, p.Diagnostics, 1)
	d := p.Diagnostics[0]
	assert.Contains(t, d.Message, "nmae")
	assert.Equal(t, "vela", d.Source)
	assert.Equal(t, SeverityError, d.Severity)
	// Line 6, "\toutput: metadata: name: context.nmae": nmae starts at byte 33.
	assert.Equal(t, Range{Start: Position{Line: 5, Character: 33}, End: Position{Line: 5, Character: 37}}, d.Range)

	c.send("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []TextDocumentContentChangeEvent{{Text: fixedDef}},
	}, false)
	assert.Empty(t, c.diagnostics().Diagnostics)

	c.send("textDocument/didClose", DidCloseTextDocumentParams{TextDocument: TextDocumentIdentifier{URI: uri}}, false)
	assert.Empty(t, c.diagnostics().Diagnostics)

	c.send("shutdown", nil, true)
	m = c.read()
	assert.Equal(t, `null`, string(m["result"]))
	c.send("exit", nil, false)
	require.NoError(t, <-c.done)
}

func TestServerIgnoresPlainCUE(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: "file:///x/values.cue", LanguageID: "cue", Version: 1, Text: "a: {\n",
	}}, false)
	assert.Empty(t, c.diagnostics().Diagnostics)
}

func TestServerRejectsUnknownRequests(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/formatting", map[string]interface{}{}, true)
	m := c.read()
	var e ResponseError
	require.NoError(t, json.Unmarshal(m["error"], &e))
	assert.Equal(t, CodeMethodNotFound, e.Code)
}

func TestToProtocolPositionCountsUTF16(t *testing.T) {
	// "é" is two bytes in UTF-8 and one UTF-16 unit; "𝄞" is four bytes and two units.
	text := "a: \"é𝄞\" & x\n"
	// x is at byte column 15, UTF-16 character 11.
	got := toProtocolPosition(text, 1, 15)
	assert.Equal(t, Position{Line: 0, Character: 11}, got)
}

func TestServerSendsMarkerProblemsAsWarnings(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: uri, LanguageID: "cue", Version: 1, Text: strings.Replace(fixedDef, "template: {\n", "template: {\n\t// +usge=x\n\tparameter: a: string\n", 1),
	}}, false)
	p := c.diagnostics()
	// The misspelt marker leaves the field without +usage too.
	require.Len(t, p.Diagnostics, 2)
	assert.Equal(t, SeverityWarning, p.Diagnostics[0].Severity)
	assert.Contains(t, p.Diagnostics[0].Message, "did you mean +usage?")
	assert.Equal(t, SeverityInformation, p.Diagnostics[1].Severity)
}

func TestServerSendsRecommendationsAsInformation(t *testing.T) {
	c := newClient(t)
	c.send("textDocument/didOpen", DidOpenTextDocumentParams{TextDocument: TextDocumentItem{
		URI: uri, LanguageID: "cue", Version: 1, Text: strings.Replace(fixedDef, "template: {\n", "template: {\n\tparameter: replicas: int\n", 1),
	}}, false)
	p := c.diagnostics()
	require.Len(t, p.Diagnostics, 1)
	assert.Equal(t, SeverityInformation, p.Diagnostics[0].Severity)
	assert.Contains(t, p.Diagnostics[0].Message, "replicas has no +usage")
}
