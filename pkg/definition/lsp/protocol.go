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

import "encoding/json"

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
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
)

// Diagnostic is a problem in a document.
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	Source   string             `json:"source"`
	Message  string             `json:"message"`
	// Data carries the diagnostic's fixes, for the client to hand back
	// with a code action request.
	Data json.RawMessage `json:"data,omitempty"`
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
	TextDocumentSync TextDocumentSyncOptions `json:"textDocumentSync"`
	// Experimental carries what is particular to this server, for its client.
	Experimental       *ExperimentalCapabilities `json:"experimental,omitempty"`
	CompletionProvider *CompletionOptions        `json:"completionProvider,omitempty"`
	HoverProvider      bool                      `json:"hoverProvider,omitempty"`
	CodeActionProvider bool                      `json:"codeActionProvider,omitempty"`
	DefinitionProvider bool                      `json:"definitionProvider,omitempty"`
	ReferencesProvider bool                      `json:"referencesProvider,omitempty"`
	RenameProvider     bool                      `json:"renameProvider,omitempty"`
	// DocumentSymbolProvider offers a document's outline.
	DocumentSymbolProvider bool `json:"documentSymbolProvider,omitempty"`
	// InlayHintProvider offers labels shown in the text.
	InlayHintProvider bool `json:"inlayHintProvider,omitempty"`
	// CodeLensProvider offers commands shown above lines.
	CodeLensProvider *CodeLensOptions `json:"codeLensProvider,omitempty"`
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

// Kinds of completion.
const (
	CompletionItemKindFunction CompletionItemKind = 3
	CompletionItemKindField    CompletionItemKind = 5
	CompletionItemKindModule   CompletionItemKind = 9
	CompletionItemKindKeyword  CompletionItemKind = 14
)

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
	Label      string             `json:"label"`
	Kind       CompletionItemKind `json:"kind"`
	Detail     string             `json:"detail,omitempty"`
	FilterText string             `json:"filterText,omitempty"`
	// InsertTextFormat is 2 when TextEdit's text is a snippet.
	InsertTextFormat int           `json:"insertTextFormat,omitempty"`
	Documentation    MarkupContent `json:"documentation"`
	TextEdit         TextEdit      `json:"textEdit"`
}

// CompletionList is the completions at a position.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// VelaProtocol is the version of the vela/* methods this server answers.
const VelaProtocol = 5

// ExperimentalCapabilities are this server's own capabilities.
type ExperimentalCapabilities struct {
	// VelaProtocol is the version of the vela/* requests and notifications
	// the server answers, raised whenever one is added or changed.
	VelaProtocol int `json:"velaProtocol"`
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

// InitializeParams is what the server reads of initialize.
type InitializeParams struct {
	RootURI               string            `json:"rootUri"`
	WorkspaceFolders      []WorkspaceFolder `json:"workspaceFolders"`
	InitializationOptions Settings          `json:"initializationOptions"`
}

// Settings are the client's kubevela settings the server acts on.
type Settings struct {
	// ValidateOutputs is auto, on or off.
	ValidateOutputs string `json:"validateOutputs"`
	// ReadCluster, when false, keeps the server from reading the
	// kubeconfig's cluster at all. Unset is true.
	ReadCluster *bool `json:"readCluster,omitempty"`
	// WorkspaceDiagnostics, when false, keeps the server to checking open
	// documents. Unset is true.
	WorkspaceDiagnostics *bool `json:"workspaceDiagnostics,omitempty"`
}

// DidChangeConfigurationParams of workspace/didChangeConfiguration.
type DidChangeConfigurationParams struct {
	Settings struct {
		KubeVela Settings `json:"kubevela"`
	} `json:"settings"`
}

// WorkspaceFolder is one root of the editor's workspace.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// FileChangeType says what happened to a watched file.
type FileChangeType int

// Watched file changes.
const (
	FileChangeCreated FileChangeType = 1
	FileChangeChanged FileChangeType = 2
	FileChangeDeleted FileChangeType = 3
)

// FileEvent is one change to a watched file.
type FileEvent struct {
	URI  string         `json:"uri"`
	Type FileChangeType `json:"type"`
}

// DidChangeWatchedFilesParams of workspace/didChangeWatchedFiles.
type DidChangeWatchedFilesParams struct {
	Changes []FileEvent `json:"changes"`
}

// MethodDefinitions lists the workspace's definitions of a type, by name.
// It is this server's own request.
const MethodDefinitions = "vela/definitions"

// DefinitionsParams names the type to list.
type DefinitionsParams struct {
	Type string `json:"type"`
}

// DefinitionsResult is the names of the definitions found.
type DefinitionsResult struct {
	Names []string `json:"names"`
}

// HoverParams asks what is at a position.
type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Hover is what is at a position, as Markdown.
type Hover struct {
	Contents MarkupContent `json:"contents"`
}

// MethodNewTest scaffolds the test file of a definition. It is this
// server's own request.
const MethodNewTest = "vela/newTest"

// NewTestParams names the definition, with its text when it is open.
type NewTestParams struct {
	// TextDocument is the definition, or a test file of it.
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text,omitempty"`
	// Function is the kind of test, as #ComponentStatus; empty for the
	// definition type's first.
	Function string `json:"function,omitempty"`
}

// NewTestResult is where the test file goes and its text, as a snippet,
// and its cases alone, to add to a test file that exists.
type NewTestResult struct {
	Path    string `json:"path"`
	Snippet string `json:"snippet"`
	Cases   string `json:"cases"`
}

// MethodTestKinds lists the kinds of test a definition can have.
const MethodTestKinds = "vela/testKinds"

// TestKindsParams name the definition.
type TestKindsParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// TestKindsResult is the kinds, each a test function and what it checks.
type TestKindsResult struct {
	Kinds []TestKindItem `json:"kinds"`
}

// TestKindItem is a kind of test.
type TestKindItem struct {
	Function string `json:"function"`
	Label    string `json:"label"`
	Doc      string `json:"doc,omitempty"`
}

// MethodNewPackage scaffolds a Package resource. It is this server's own
// request.
const MethodNewPackage = "vela/newPackage"

// MethodComponentTypes lists the definitions of a type an Application can
// name, components by default: the workspace's, the cluster's and the
// built-in ones, each once.
const MethodComponentTypes = "vela/componentTypes"

// ComponentTypesParams name the definition type: component, trait, policy
// or workflow-step.
type ComponentTypesParams struct {
	Type string `json:"type,omitempty"`
}

// ComponentType is one, with where it comes from.
type ComponentType struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
}

// ComponentTypesResult is the component types, by name.
type ComponentTypesResult struct {
	Types []ComponentType `json:"types"`
}

// CodeLensOptions are the server's code lenses' options.
type CodeLensOptions struct{}

// CodeLensParams ask for a document's code lenses.
type CodeLensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// Command is a command a client runs.
type Command struct {
	Title     string        `json:"title"`
	Command   string        `json:"command"`
	Arguments []interface{} `json:"arguments,omitempty"`
}

// CodeLens is a command shown above a range.
type CodeLens struct {
	Range   Range    `json:"range"`
	Command *Command `json:"command,omitempty"`
}

// CommandAddToApplication is the client's command a code lens of an
// Application runs, with an AddToApplicationParams for argument.
const CommandAddToApplication = "kubevela.addToApplication"

// MethodAddToApplication is the edit adding a component, trait, policy or
// workflow step to an Application.
const MethodAddToApplication = "vela/addToApplication"

// AddToApplicationParams name the Application by a line in it, what to add
// and, for a trait, the component by a line in it.
type AddToApplicationParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Line         uint32                 `json:"line"`
	Kind         string                 `json:"kind"`
	Type         string                 `json:"type,omitempty"`
	Name         string                 `json:"name,omitempty"`
}

// AddToApplicationResult is the range to replace and the snippet to put
// there.
type AddToApplicationResult struct {
	Range   Range  `json:"range"`
	Snippet string `json:"snippet"`
}

// MethodNewApplication scaffolds an Application of one component.
const MethodNewApplication = "vela/newApplication"

// NewApplicationParams name the Application and its component's type.
type NewApplicationParams struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// NewApplicationResult is its YAML, as a snippet.
type NewApplicationResult struct {
	Snippet string `json:"snippet"`
}

// MethodSource is the vela/source request: the text of a read-only document
// of what KubeVela declares, at a vela-source: URI.
const MethodSource = "vela/source"

// SourceParams name the read-only document.
type SourceParams struct {
	URI string `json:"uri"`
}

// SourceResult is its text.
type SourceResult struct {
	Text string `json:"text"`
}

// NewPackageParams name the package, its import path, and the protocol its
// provider speaks, or none for a package of plain CUE.
type NewPackageParams struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Protocol string `json:"protocol,omitempty"`
}

// NewPackageResult is the Package resource's YAML.
type NewPackageResult struct {
	YAML string `json:"yaml"`
}

// CodeActionParams asks for the actions at a range.
type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

// CodeActionContext holds the diagnostics at the range.
type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// CodeAction is an action the client may take, as an edit.
type CodeAction struct {
	Title       string        `json:"title"`
	Kind        string        `json:"kind"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
	IsPreferred bool          `json:"isPreferred,omitempty"`
	Edit        WorkspaceEdit `json:"edit"`
}

// WorkspaceEdit is a set of edits, by document.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// MethodClusterStatus tells the client what the server reads of the
// kubeconfig's cluster, whenever that changes. It is this server's own
// notification.
const MethodClusterStatus = "vela/clusterStatus"

// MethodReconnectCluster asks the server to read the kubeconfig's cluster
// again, as after its context changes. It is this server's own request.
const MethodReconnectCluster = "vela/reconnectCluster"

// ClusterStatus is what the server reads of the kubeconfig's cluster.
type ClusterStatus struct {
	// Enabled is false when the readCluster setting turns reading it off.
	Enabled bool `json:"enabled"`
	// Context is the kubeconfig context read.
	Context string `json:"context,omitempty"`
	// Reachable is set when the cluster answered.
	Reachable bool `json:"reachable"`
	// KubeVela is set when it runs KubeVela, so its kinds are read.
	KubeVela bool `json:"kubeVela"`
	// Packages is how many Package resources were read from it.
	Packages int `json:"packages"`
	// Error is why it was not reached.
	Error string `json:"error,omitempty"`
}

// TextDocumentPositionParams name a position in a document.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Location is a range in a document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// ReferenceParams ask for the references to what is at a position.
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext       `json:"context"`
}

// ReferenceContext says whether the declaration counts as a reference.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// RenameParams ask to rename what is at a position.
type RenameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// DocumentSymbol is an entry of a document's outline.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// DocumentSymbolParams name a document.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// diagnosticFix is a fix as a diagnostic's data carries it.
type diagnosticFix struct {
	Title string     `json:"title"`
	Edits []TextEdit `json:"edits"`
}

// InlayHintParams ask for the hints in a range of a document.
type InlayHintParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

// InlayHint is a label shown in the text.
type InlayHint struct {
	Position    Position `json:"position"`
	Label       string   `json:"label"`
	Kind        int      `json:"kind,omitempty"`
	Tooltip     string   `json:"tooltip,omitempty"`
	PaddingLeft bool     `json:"paddingLeft,omitempty"`
}

// MethodClusterDefinition asks for the definition a document defines, as
// applied to the cluster. It is this server's own request.
const MethodClusterDefinition = "vela/clusterDefinition"

// ClusterDefinitionParams name the document.
type ClusterDefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// ClusterDefinitionResult is the applied definition in CUE, where it is.
type ClusterDefinitionResult struct {
	CUE       string `json:"cue"`
	Namespace string `json:"namespace"`
	Context   string `json:"context"`
}

// MethodDefinitionFiles lists the files of the workspace's definitions. It
// is this server's own request.
const MethodDefinitionFiles = "vela/definitionFiles"

// DefinitionFilesResult is the files, in order.
type DefinitionFilesResult struct {
	Files []DefinitionFile `json:"files"`
}

// DefinitionFile is a file of the workspace and the definition it defines.
type DefinitionFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
}
