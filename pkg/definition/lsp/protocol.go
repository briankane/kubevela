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

// The subset of the Language Server Protocol 3.17 this server speaks. Field
// names and values follow the specification.

// Position is a 0-based line and UTF-16 character offset.
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Range is a span in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// DiagnosticSeverity of a Diagnostic.
type DiagnosticSeverity int

// Severities of a diagnostic.
const (
	SeverityError   DiagnosticSeverity = 1
	SeverityWarning DiagnosticSeverity = 2
)

// Diagnostic is a problem in a document.
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	Source   string             `json:"source"`
	Message  string             `json:"message"`
}

// PublishDiagnosticsParams replaces every diagnostic of a document.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// TextDocumentItem is a document as opened.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentIdentifier names a document.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier names a version of a document.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// TextDocumentContentChangeEvent is the whole new text, since the server
// asks for full sync.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

// DidOpenTextDocumentParams of textDocument/didOpen.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// DidChangeTextDocumentParams of textDocument/didChange.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// DidCloseTextDocumentParams of textDocument/didClose.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// TextDocumentSyncKind is how document changes are sent.
type TextDocumentSyncKind int

// TextDocumentSyncFull sends the whole document on every change.
const TextDocumentSyncFull TextDocumentSyncKind = 1

// TextDocumentSyncOptions is the server's sync capability.
type TextDocumentSyncOptions struct {
	OpenClose bool                 `json:"openClose"`
	Change    TextDocumentSyncKind `json:"change"`
}

// ServerCapabilities the server announces.
type ServerCapabilities struct {
	TextDocumentSync   TextDocumentSyncOptions `json:"textDocumentSync"`
	CompletionProvider *CompletionOptions      `json:"completionProvider,omitempty"`
}

// CompletionOptions is the server's completion capability.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

// CompletionParams asks for completions at a position.
type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CompletionItemKind of a CompletionItem.
type CompletionItemKind int

// CompletionItemKindKeyword marks a marker or one of its values.
const CompletionItemKindKeyword CompletionItemKind = 14

// MarkupContent is documentation, as Markdown.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// TextEdit replaces a range with new text.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// CompletionItem is one completion.
type CompletionItem struct {
	Label         string             `json:"label"`
	Kind          CompletionItemKind `json:"kind"`
	FilterText    string             `json:"filterText,omitempty"`
	Documentation MarkupContent      `json:"documentation"`
	TextEdit      TextEdit           `json:"textEdit"`
}

// CompletionList is the completions at a position.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// ServerInfo names the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult answers initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

// ResponseError is a JSON-RPC error.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC and LSP error codes.
const (
	CodeParseError     = -32700
	CodeInvalidParams  = -32602
	CodeMethodNotFound = -32601
	CodeInvalidRequest = -32600
	// CodeRequestCancelled answers a request that a newer one replaced.
	CodeRequestCancelled = -32800
)

// MethodRenderDefKit renders a DefKit (Go) definition file to CUE. It is this
// server's own request, not part of LSP.
const MethodRenderDefKit = "vela/renderDefKit"

// RenderDefKitParams names the Go file to render. It is read from disk, so it
// renders as last saved.
type RenderDefKitParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// RenderDefKitResult is what a Go file renders to: an error when the file
// could not be loaded at all, otherwise one entry per definition in it.
type RenderDefKitResult struct {
	Error       string               `json:"error,omitempty"`
	Definitions []RenderedDefinition `json:"definitions"`
}

// RenderedDefinition is one definition's CUE, or why it could not be
// generated, with the diagnostics of that CUE at positions within it.
type RenderedDefinition struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"`
	CUE         string       `json:"cue,omitempty"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// MethodPreviewOutput renders a definition with sample values: what it
// produces, as the controller would render it. It is this server's own request.
const MethodPreviewOutput = "vela/previewOutput"

// PreviewOutputParams is a definition's current text and the values, YAML, to
// render it with. The answer is a preview.Result.
type PreviewOutputParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text"`
	Values       string                 `json:"values"`
}

// MethodPreviewValues writes a values file for a definition: its defaults
// filled in and each required parameter named.
const MethodPreviewValues = "vela/previewValues"

// PreviewValuesParams is a definition's current text.
type PreviewValuesParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text"`
}

// PreviewValuesResult is a values file, YAML.
type PreviewValuesResult struct {
	YAML string `json:"yaml"`
}

// MethodTestCases lists the cases of a CUE test file (*_test.cue) without
// running them. It is this server's own request.
const MethodTestCases = "vela/testCases"

// TestCasesParams names a test file, with its text when the editor has it
// open; without text the file is read from disk.
type TestCasesParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

// TestCasesResult is the file's cases, or why it did not load.
type TestCasesResult struct {
	Error string     `json:"error,omitempty"`
	Cases []TestCase `json:"cases"`
}

// TestCase is one case of a test file. Line is 1-based, as `vela def test`
// reports it and --focus-file takes it; Range is the case's name.
type TestCase struct {
	Name          string   `json:"name"`
	Line          int      `json:"line"`
	Range         Range    `json:"range"`
	Definition    string   `json:"definition"`
	Test          string   `json:"test"`
	Labels        []string `json:"labels"`
	Pending       bool     `json:"pending,omitempty"`
	PendingReason string   `json:"pendingReason,omitempty"`
}
