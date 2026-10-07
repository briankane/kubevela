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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"

	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/goloader"
	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
	"github.com/oam-dev/kubevela/pkg/definition/preview"
	"github.com/oam-dev/kubevela/pkg/utils"
	"github.com/oam-dev/kubevela/version"
)

// diagnosticSource names this server in the editor's problem list.
const diagnosticSource = "vela"

// Server answers one client.
type Server struct {
	mu       sync.Mutex
	out      io.Writer
	shutdown bool
	// docs is the text of each open document. Only the message loop uses it.
	docs map[string]string
	// folders are the workspace's roots; published is what each global policy
	// in them publishes, by file path. Only the message loop uses them.
	folders   []string
	published map[string]analysis.Published
	// definitions and packages are what each workspace file holds of them;
	// externals are all the packages, for checks to import.
	definitions map[string]definitionEntry
	packages    map[string][]cuexruntime.Package
	externals   *analysis.Externals
	// clusterPackages are the cluster's Package resources.
	clusterPackages []cuexruntime.Package
	// clusterDefs are the cluster's definitions an Application may name.
	clusterDefs *clusterDefinitions
	// crds are the CustomResourceDefinitions each workspace file holds.
	crds map[string][][]byte

	// validateOutputs is the kubevela.validateOutputs setting: auto, on or
	// off. kinds are the schemas outputs are checked against, nil when off.
	validateOutputs string
	// clusterEnabled is the readCluster setting.
	clusterEnabled bool
	// workspaceDiags is the workspaceDiagnostics setting; workspaceFiles are
	// the files it checks; checkGen counts the passes over them, so a newer
	// pass supersedes an older one.
	workspaceDiags bool
	workspaceFiles map[string]bool
	checkGen       int
	connect        ClusterConnector
	cluster        *clusterState
	kinds          *kubeschema.Schemas
	// events carries work back to the message loop from goroutines.
	events chan func()

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

// WithCluster replaces how the kubeconfig's cluster is reached.
func WithCluster(c ClusterConnector) Option {
	return func(s *Server) { s.connect = c }
}

// WithGoRenderer replaces the renderer of DefKit files, goloader.LoadFromFile.
func WithGoRenderer(r GoRenderer) Option {
	return func(s *Server) { s.renderGo = r }
}

// NewServer returns a server for one client.
func NewServer(opts ...Option) *Server {
	s := &Server{
		renderGo:        goloader.LoadFromFile,
		renders:         map[string]int{},
		docs:            map[string]string{},
		published:       map[string]analysis.Published{},
		crds:            map[string][][]byte{},
		validateOutputs: validateAuto,
		clusterEnabled:  true,
		workspaceDiags:  true,
		workspaceFiles:  map[string]bool{},
		connect:         ConnectKubeconfig,
		definitions:     map[string]definitionEntry{},
		packages:        map[string][]cuexruntime.Package{},
		events:          make(chan func(), 16),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Serve answers messages from in on out until the client sends exit, in is
// closed, or ctx is done. Messages, and work goroutines hand back, are handled
// in order on one loop, so each document's diagnostics are published for its
// latest text.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	done := make(chan struct{})
	defer close(done)
	bodies := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			body, err := readMessage(r)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case bodies <- body:
			case <-done:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case work := <-s.events:
			work()
		case body := <-bodies:
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
}

// velaRequest answers the server's own vela/* requests that reply at once.
func (s *Server) velaRequest(msg message) (result interface{}, rerr *ResponseError) {
	switch msg.Method {
	case MethodDefinitionFiles:
		files := make([]DefinitionFile, 0, len(s.definitions))
		for path, d := range s.definitions {
			files = append(files, DefinitionFile{Path: path, Name: d.name, Type: d.defType})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		result = DefinitionFilesResult{Files: files}
	case MethodReconnectCluster:
		s.reconnectCluster()
		result = struct{}{}
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
	case MethodDefinitions:
		var p DefinitionsParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			result = DefinitionsResult{Names: append([]string{}, s.definitionNames(p.Type)...)}
		}
	case MethodSource:
		var p SourceParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			text, ok := analysis.Source(p.URI, s.externals)
			if !ok {
				rerr = &ResponseError{Code: CodeInvalidParams, Message: "no such document: " + p.URI}
			} else {
				result = SourceResult{Text: text}
			}
		}
	case MethodNewPackage:
		var p NewPackageParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			result = NewPackageResult{YAML: analysis.NewPackage(p.Name, p.Path, p.Protocol)}
		}
	case MethodNewTest:
		var p NewTestParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			path := pathOf(p.TextDocument.URI)
			src := []byte(p.Text)
			if len(src) == 0 {
				//nolint:gosec // reading the definition the client names is the point
				src, _ = os.ReadFile(path)
			}
			file, snippet, ok := analysis.NewTestFile(path, src, testExternals())
			if !ok {
				rerr = &ResponseError{Code: CodeInvalidParams, Message: "not a definition KubeVela's tests can run: a component, trait, policy, workflow step or source"}
			} else {
				result = NewTestResult{Path: file, Snippet: snippet}
			}
		}
	case MethodTestCases:
		var p TestCasesParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			result = testCases(p)
		}
	}
	return result, rerr
}

// laterRequest starts answering a request that replies once work off the
// message loop is done: a DefKit render runs Go, and reading a definition
// from the cluster waits on it.
func (s *Server) laterRequest(msg message) *ResponseError {
	switch msg.Method {
	case MethodClusterDefinition:
		var p ClusterDefinitionParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return rerr
		}
		s.clusterDefinition(msg.ID, p)
	case MethodRenderDefKit:
		var p RenderDefKitParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return rerr
		}
		// A render runs Go and takes seconds, so it answers when it is done
		// while other messages are handled.
		go s.renderDefKit(msg.ID, pathOf(p.TextDocument.URI))
	}
	return nil
}

// hover describes what is at a position: in YAML, a key or an
// Application's type; in a status field, what it reads; in an addon's CUE or
// a definition, what is declared there.
func (s *Server) hover(p HoverParams) (Hover, bool) {
	text := s.docs[p.TextDocument.URI]
	path := pathOf(p.TextDocument.URI)
	offset := byteOffset(text, p.Position)
	opts := s.options()
	var h string
	var ok bool
	switch ext := filepath.Ext(path); {
	case ext == ".yaml" || ext == ".yml":
		h, ok = analysis.HoverYAMLFile(path, text, offset, opts)
	default:
		if h, ok = analysis.HoverStatusField(path, text, offset, opts); ok {
			break
		}
		if _, _, isAddon := analysis.AddonFileKind(path); isAddon {
			h, ok = analysis.HoverAddonFile(path, text, offset, opts)
		} else {
			h, ok = analysis.Hover(text, offset, opts)
		}
	}
	if !ok {
		return Hover{}, false
	}
	return Hover{Contents: MarkupContent{Kind: "markdown", Value: h}}, true
}

// configure acts on changed settings.
func (s *Server) configure(set Settings) {
	if set.WorkspaceDiagnostics != nil && *set.WorkspaceDiagnostics != s.workspaceDiags {
		s.workspaceDiags = *set.WorkspaceDiagnostics
		if s.workspaceDiags {
			s.checkWorkspace()
		} else {
			s.clearWorkspace()
		}
	}
	if set.ReadCluster != nil && *set.ReadCluster != s.clusterEnabled {
		s.clusterEnabled = *set.ReadCluster
		if !s.clusterEnabled {
			s.dropCluster()
		} else {
			s.reconnectCluster()
		}
	}
	if set.ValidateOutputs != "" && set.ValidateOutputs != s.validateOutputs {
		s.validateOutputs = set.ValidateOutputs
		s.rebuildKinds()
		s.republish()
	}
}

// post hands work to the message loop.
func (s *Server) post(work func()) {
	s.events <- work
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
		var p InitializeParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &p)
		}
		s.folders = workspaceFolders(p)
		if p.InitializationOptions.ValidateOutputs != "" {
			s.validateOutputs = p.InitializationOptions.ValidateOutputs
		}
		if p.InitializationOptions.ReadCluster != nil {
			s.clusterEnabled = *p.InitializationOptions.ReadCluster
		}
		if p.InitializationOptions.WorkspaceDiagnostics != nil {
			s.workspaceDiags = *p.InitializationOptions.WorkspaceDiagnostics
		}
		result = InitializeResult{
			Capabilities: ServerCapabilities{
				TextDocumentSync: TextDocumentSyncOptions{
					OpenClose: true,
					Change:    TextDocumentSyncFull,
				},
				HoverProvider:      true,
				CompletionProvider: &CompletionOptions{TriggerCharacters: []string{"+", ":", "=", ".", "/", "\""}},
				Experimental:       &ExperimentalCapabilities{VelaProtocol: VelaProtocol},
			},
			ServerInfo: ServerInfo{Name: "vela-def-lsp", Version: version.VelaVersion},
		}
	case "initialized":
		s.rebuildKinds()
		s.indexWorkspace()
		s.connectCluster()
	case "workspace/didChangeConfiguration":
		var p DidChangeConfigurationParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			s.configure(p.Settings.KubeVela)
		}
	case "workspace/didChangeWatchedFiles":
		var p DidChangeWatchedFilesParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			s.watchedFilesChanged(p.Changes)
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
	case MethodClusterDefinition, MethodRenderDefKit:
		if rerr = s.laterRequest(msg); rerr == nil {
			return nil
		}
	case MethodPreviewOutput, MethodPreviewValues, MethodDefinitions, MethodTestCases, MethodNewTest, MethodNewPackage, MethodReconnectCluster, MethodDefinitionFiles, MethodSource:
		result, rerr = s.velaRequest(msg)
	case "textDocument/hover":
		var p HoverParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			if h, ok := s.hover(p); ok {
				result = h
			}
		}
	case "textDocument/definition", "textDocument/references", "textDocument/rename", "textDocument/codeAction", "textDocument/documentSymbol", "textDocument/inlayHint":
		result, rerr = s.navigationRequest(msg)
	case "textDocument/completion":
		var p CompletionParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			result = s.completions(p.TextDocument.URI, s.docs[p.TextDocument.URI], p.Position)
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if rerr = decode(msg.Params, &p); rerr == nil {
			delete(s.docs, p.TextDocument.URI)
			s.reindexFromDisk(pathOf(p.TextDocument.URI))
			return s.publishClosed(p.TextDocument.URI)
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
	s.post(func() {
		checkRendered(path, &result)
		_ = s.reply(id, result, nil)
	})
}

// checkRendered checks the CUE each DefKit definition rendered to.
func checkRendered(path string, result *RenderDefKitResult) {
	for i, d := range result.Definitions {
		if d.CUE != "" {
			result.Definitions[i].Diagnostics = diagnose(path+"#"+d.Name+".cue", d.CUE, analysis.Options{})
		}
	}
}

// renderGo renders a DefKit file and checks each definition's CUE as a
// hand-written definition is checked.
// renderGo renders the DefKit definitions of a Go file. It runs Go, so off
// the message loop; checking what it rendered compiles CUE, so is left to
// checkRendered, on the loop.
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
		}
		out.Definitions = append(out.Definitions, d)
	}
	return out
}

// update keeps a document's new text and publishes its diagnostics.
func (s *Server) update(uri, text string, version *int) error {
	s.docs[uri] = text
	s.index(pathOf(uri), []byte(text))
	return s.publish(PublishDiagnosticsParams{URI: uri, Version: version, Diagnostics: s.diagnose(uri, text)})
}

// diagnose checks a document with what the workspace offers.
func (s *Server) diagnose(uri, text string) []Diagnostic {
	return append(diagnose(uri, text, s.options()), s.duplicateDefinition(pathOf(uri), text)...)
}

// diagnose analyses a document and converts the result to protocol positions.
// A CUE test file is checked by loading it, as `vela def test` would.
func diagnose(uri, text string, opts analysis.Options) []Diagnostic {
	path := pathOf(uri)
	if utils.IsCUETestFile(path) {
		return testDiagnostics(path, text)
	}
	found, addonFile := analysis.CheckAddonFile(path, []byte(text), opts)
	if filepath.Ext(path) != ".cue" {
		// A Package may sit anywhere, an addon's resources included.
		if pkgDiags, ok := analysis.CheckPackageFile(path, []byte(text), opts.Externals); ok {
			found = append(found, pkgDiags...)
		}
		if appDiags, ok := analysis.CheckApplicationFile(path, []byte(text), opts); ok {
			found = append(found, appDiags...)
		}
		return toProtocol(text, found)
	}
	if addonFile {
		return toProtocol(text, found)
	}
	res := analysis.AnalyzeWith(path, []byte(text), opts)
	diags := make([]Diagnostic, 0, len(res.Diagnostics))
	for _, d := range res.Diagnostics {
		diags = append(diags, Diagnostic{
			Range: Range{
				Start: toProtocolPosition(text, d.Range.Start.Line, d.Range.Start.Column),
				End:   toProtocolPosition(text, d.Range.End.Line, d.Range.End.Column),
			},
			Severity: severity(d.Severity),
			Source:   diagnosticSource,
			Message:  d.Message,
			Data:     fixesData(text, d.Fixes),
		})
	}
	return diags
}

func severity(s analysis.Severity) DiagnosticSeverity {
	switch s {
	case analysis.SeverityWarning:
		return SeverityWarning
	case analysis.SeverityInfo:
		return SeverityInformation
	case analysis.SeverityError:
	}
	return SeverityError
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
