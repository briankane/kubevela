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

// Package lsp is a language server for CUE X-Definitions, speaking the
// Language Server Protocol over a pair of streams. The checking itself is
// done by package analysis.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/goloader"
	"github.com/oam-dev/kubevela/pkg/definition/preview"
	"github.com/oam-dev/kubevela/version"
)

// diagnosticSource names this server in the editor's problem list.
const diagnosticSource = "vela"

// Server answers one client.
type Server struct {
	mu       sync.Mutex
	out      io.Writer
	shutdown bool

	renderGo GoRenderer
	// renders counts the renders asked of each Go file, so only the latest
	// one's result is sent.
	renders   map[string]int
	rendersMu sync.Mutex
}

// GoRenderer generates the CUE of each definition in a DefKit Go file.
type GoRenderer func(path string) ([]goloader.LoadResult, error)

// Option configures a Server.
type Option func(*Server)

// WithGoRenderer replaces the renderer of DefKit files, goloader.LoadFromFile.
func WithGoRenderer(r GoRenderer) Option {
	return func(s *Server) { s.renderGo = r }
}

// NewServer returns a server for one client.
func NewServer(opts ...Option) *Server {
	s := &Server{renderGo: goloader.LoadFromFile, renders: map[string]int{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Serve answers messages from in on out until the client sends exit, in is
// closed, or ctx is done. Messages are handled in order, so each document's
// diagnostics are published for its latest text.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	r := bufio.NewReader(in)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := readMessage(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var msg message
		if err := json.Unmarshal(body, &msg); err != nil {
			if err := s.reply(nil, nil, &ResponseError{Code: CodeParseError, Message: err.Error()}); err != nil {
				return err
			}
			continue
		}
		if msg.Method == "exit" {
			return nil
		}
		if err := s.handle(msg); err != nil {
			return err
		}
	}
}

func (s *Server) handle(msg message) error {
	isRequest := len(msg.ID) > 0
	var (
		result interface{}
		rerr   *ResponseError
	)
	if s.shutdown && isRequest {
		return s.reply(msg.ID, nil, &ResponseError{Code: CodeInvalidRequest, Message: "server is shut down"})
	}
	switch msg.Method {
	case "initialize":
		result = InitializeResult{
			Capabilities: ServerCapabilities{TextDocumentSync: TextDocumentSyncOptions{
				OpenClose: true,
				Change:    TextDocumentSyncFull,
			}},
			ServerInfo: ServerInfo{Name: "vela-def-lsp", Version: version.VelaVersion},
		}
	case "shutdown":
		s.shutdown = true
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			return s.update(p.TextDocument.URI, p.TextDocument.Text, &p.TextDocument.Version)
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if rerr = decode(msg.Params, &p); rerr == nil && len(p.ContentChanges) > 0 {
			text := p.ContentChanges[len(p.ContentChanges)-1].Text
			return s.update(p.TextDocument.URI, text, &p.TextDocument.Version)
		}
	case MethodRenderDefKit:
		var p RenderDefKitParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			// A render runs Go and takes seconds, so it answers when it is done
			// while other messages are handled.
			go s.renderDefKit(msg.ID, pathOf(p.TextDocument.URI))
			return nil
		}
	case MethodPreviewOutput:
		var p PreviewOutputParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			result = preview.Render(context.Background(), preview.Request{Path: pathOf(p.TextDocument.URI), Source: []byte(p.Text), Values: []byte(p.Values)})
		}
	case MethodPreviewValues:
		var p PreviewValuesParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			yaml, err := preview.Skeleton(context.Background(), pathOf(p.TextDocument.URI), []byte(p.Text))
			if err != nil {
				rerr = &ResponseError{Code: CodeInvalidParams, Message: err.Error()}
			} else {
				result = PreviewValuesResult{YAML: yaml}
			}
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			return s.publish(PublishDiagnosticsParams{URI: p.TextDocument.URI, Diagnostics: []Diagnostic{}})
		}
	default:
		if isRequest {
			rerr = &ResponseError{Code: CodeMethodNotFound, Message: "method not supported: " + msg.Method}
		}
	}
	if !isRequest {
		return nil
	}
	return s.reply(msg.ID, result, rerr)
}

// renderDefKit answers a render request for path, unless a newer one for the
// same file was made while it ran.
func (s *Server) renderDefKit(id json.RawMessage, path string) {
	s.rendersMu.Lock()
	s.renders[path]++
	n := s.renders[path]
	s.rendersMu.Unlock()

	result := renderGo(s.renderGo, path)

	s.rendersMu.Lock()
	latest := s.renders[path] == n
	s.rendersMu.Unlock()
	if !latest {
		_ = s.reply(id, nil, &ResponseError{Code: CodeRequestCancelled, Message: "a newer render of " + path + " was asked for"})
		return
	}
	_ = s.reply(id, result, nil)
}

// renderGo renders a DefKit file and checks each definition's CUE as a
// hand-written definition is checked.
func renderGo(render GoRenderer, path string) RenderDefKitResult {
	loaded, err := render(path)
	if err != nil {
		return RenderDefKitResult{Error: err.Error(), Definitions: []RenderedDefinition{}}
	}
	out := RenderDefKitResult{Definitions: make([]RenderedDefinition, 0, len(loaded))}
	for _, l := range loaded {
		d := RenderedDefinition{Name: l.Definition.Name, Type: l.Definition.Type, Diagnostics: []Diagnostic{}}
		if l.Error != nil {
			d.Error = l.Error.Error()
		} else {
			d.CUE = l.CUE
			d.Diagnostics = diagnose(path+"#"+l.Definition.Name+".cue", l.CUE)
		}
		out.Definitions = append(out.Definitions, d)
	}
	return out
}

// update publishes the diagnostics of a document's new text.
func (s *Server) update(uri, text string, version *int) error {
	return s.publish(PublishDiagnosticsParams{URI: uri, Version: version, Diagnostics: diagnose(uri, text)})
}

// diagnose analyses a document and converts the result to protocol positions.
func diagnose(uri, text string) []Diagnostic {
	res := analysis.Analyze(pathOf(uri), []byte(text))
	diags := make([]Diagnostic, 0, len(res.Diagnostics))
	for _, d := range res.Diagnostics {
		diags = append(diags, Diagnostic{
			Range: Range{
				Start: toProtocolPosition(text, d.Range.Start.Line, d.Range.Start.Column),
				End:   toProtocolPosition(text, d.Range.End.Line, d.Range.End.Column),
			},
			Severity: SeverityError,
			Source:   diagnosticSource,
			Message:  d.Message,
		})
	}
	return diags
}

// pathOf is the file path of a file URI, used only to name the file in
// positions; anything else is used as is.
func pathOf(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return uri
}

// toProtocolPosition converts a 1-based line and byte column to a 0-based
// line and UTF-16 character, which is what LSP counts in.
func toProtocolPosition(text string, line, column int) Position {
	if line < 1 {
		return Position{}
	}
	start := 0
	for l := 1; l < line && start < len(text); l++ {
		i := strings.IndexByte(text[start:], '\n')
		if i < 0 {
			start = len(text)
			break
		}
		start += i + 1
	}
	end := start + column - 1
	if end > len(text) {
		end = len(text)
	}
	units := 0
	for _, r := range text[start:end] {
		units += utf16.RuneLen(r)
	}
	return Position{Line: uint32(line - 1), Character: uint32(units)}
}

func decode(raw json.RawMessage, v interface{}) *ResponseError {
	if err := json.Unmarshal(raw, v); err != nil {
		return &ResponseError{Code: CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

func (s *Server) publish(p PublishDiagnosticsParams) error {
	return s.write(message{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: mustJSON(p)})
}

func (s *Server) reply(id json.RawMessage, result interface{}, rerr *ResponseError) error {
	if id == nil {
		id = json.RawMessage("null")
	}
	if rerr != nil {
		return s.write(message{JSONRPC: "2.0", ID: id, Error: rerr})
	}
	// A successful response must carry result, even when it is null.
	return s.write(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  interface{}     `json:"result"`
	}{"2.0", id, result})
}

func (s *Server) write(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeMessage(s.out, v)
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
