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
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
	"github.com/oam-dev/kubevela/pkg/utils"
)

// maxIndexedFiles bounds how many CUE files the workspace index reads, so a
// huge workspace cannot hold the server up.
const maxIndexedFiles = 5000

// maxIndexedSize skips files too large to be a definition.
const maxIndexedSize = 1 << 20

// skippedDirs are not searched for definitions.
var skippedDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".vscode-test": true}

func workspaceFolders(p InitializeParams) []string {
	var folders []string
	for _, f := range p.WorkspaceFolders {
		folders = append(folders, pathOf(f.URI))
	}
	if len(folders) == 0 && p.RootURI != "" {
		folders = append(folders, pathOf(p.RootURI))
	}
	return folders
}

// indexed is what the workspace index learned of one file.
type indexed struct {
	published *analysis.Published
	// name and defType name the definition the file holds, if any.
	name, defType string
	packages      []cuexruntime.Package
	crds          [][]byte
}

// contributes reports whether a file adds anything to the index.
func (e indexed) contributes() bool {
	return e.published != nil || e.name != "" || len(e.packages) > 0 || len(e.crds) > 0
}

// indexFile reads what a file contributes to the index, from src, or from
// disk when src is nil.
func indexFile(path string, src []byte) indexed {
	var out indexed
	if src == nil {
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxIndexedSize {
			return out
		}
		//nolint:gosec // reading the workspace's own files is the point
		if src, err = os.ReadFile(path); err != nil {
			return out
		}
	}
	switch {
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if bytes.Contains(src, []byte("kind: Package")) && bytes.Contains(src, []byte("cue.oam.dev")) {
			out.packages, _ = analysis.ParsePackages(src)
		}
		if bytes.Contains(src, []byte("kind: CustomResourceDefinition")) {
			out.crds = crdDocuments(src)
		}
	case strings.HasSuffix(path, ".cue") && !utils.IsCUETestFile(path):
		out.name, out.defType, _ = analysis.DefinitionHeader(path, src)
		if p, ok := publishedIn(path, src); ok {
			out.published = &p
		}
	}
	return out
}

// indexWorkspace reads every CUE and YAML file in the workspace, on a
// goroutine, and hands what it found to the message loop.
func (s *Server) indexWorkspace() {
	folders := append([]string{}, s.folders...)
	go func() {
		found, _ := walkWorkspace(folders)
		s.post(func() {
			changed := false
			for path, entry := range found {
				if _, open := s.docs["file://"+path]; !open && entry.contributes() {
					s.record(path, entry)
					changed = true
				}
			}
			if changed {
				s.republish()
			}
		})
	}()
}

// walkWorkspace reads what each CUE and YAML file under folders
// contributes to the index, and lists the files, in order.
func walkWorkspace(folders []string) (map[string]indexed, []string) {
	found := map[string]indexed{}
	var files []string
	for _, root := range folders {
		_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil || len(files) >= maxIndexedFiles {
				return filepath.SkipDir
			}
			if e.IsDir() {
				if path != root && (skippedDirs[e.Name()] || strings.HasPrefix(e.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".cue") && !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}
			files = append(files, path)
			found[path] = indexFile(path, nil)
			return nil
		})
	}
	sort.Strings(files)
	return found, files
}

// record keeps what a file contributes to the index, replacing what it did.
func (s *Server) record(path string, entry indexed) {
	if entry.published != nil {
		s.published[path] = *entry.published
	} else {
		delete(s.published, path)
	}
	if entry.name != "" {
		s.definitions[path] = definitionEntry{name: entry.name, defType: entry.defType}
	} else {
		delete(s.definitions, path)
	}
	if len(entry.packages) > 0 {
		s.packages[path] = entry.packages
	} else {
		delete(s.packages, path)
	}
	_, hadCRDs := s.crds[path]
	if len(entry.crds) > 0 {
		s.crds[path] = entry.crds
	} else {
		delete(s.crds, path)
	}
	if hadCRDs || len(entry.crds) > 0 {
		s.rebuildKinds()
	}
	s.rebuildExternals()
}

// rebuildExternals gathers the custom provider packages checks may import:
// the workspace's, then the cluster's of an import path the workspace does
// not define, so a package being written takes precedence over the one
// applied.
func (s *Server) rebuildExternals() {
	var all []cuexruntime.Package
	files := make([]string, 0, len(s.packages))
	for p := range s.packages {
		files = append(files, p)
	}
	sort.Strings(files)
	local := map[string]bool{}
	for _, f := range files {
		for _, p := range s.packages[f] {
			local[p.GetPath()] = true
			all = append(all, p)
		}
	}
	for _, p := range s.clusterPackages {
		if !local[p.GetPath()] {
			all = append(all, p)
		}
	}
	s.externals = analysis.NewExternals(all)
}

// definitionEntry is a definition the workspace holds.
type definitionEntry struct{ name, defType string }

// options are what a check of an open document draws on from the
// workspace: its definitions, by name, and its custom provider packages. The
// lookup reads a snapshot, so it is safe off the message loop.
func (s *Server) options() analysis.Options {
	byName := map[string]string{}
	for path, d := range s.definitions {
		byName[d.name] = path
	}
	open := map[string]string{}
	for uri, text := range s.docs {
		open[uri] = text
	}
	return analysis.Options{
		Kinds:     s.kinds,
		Externals: s.externals,
		Definitions: func(name string) (string, []byte, bool) {
			path, ok := byName[name]
			if !ok {
				return "", nil, false
			}
			if text, ok := open["file://"+path]; ok {
				return path, []byte(text), true
			}
			//nolint:gosec // reading the workspace's own files is the point
			src, err := os.ReadFile(path)
			return path, src, err == nil
		},
	}
}

// definitionNames are the names of the workspace's definitions of a type.
func (s *Server) definitionNames(defType string) []string {
	var out []string
	for _, d := range s.definitions {
		if d.defType == defType {
			out = append(out, d.name)
		}
	}
	sort.Strings(out)
	return out
}

// republish checks every open document again, as what it draws on from the
// workspace has changed.
func (s *Server) republish() {
	for uri, text := range s.docs {
		_ = s.publish(PublishDiagnosticsParams{URI: uri, Diagnostics: s.diagnose(uri, text)})
	}
}

// index records what a file contributes, from its text.
func (s *Server) index(path string, src []byte) {
	s.record(path, indexFile(path, src))
}

// reindexFromDisk records what a file contributes as saved, or forgets it.
func (s *Server) reindexFromDisk(path string) {
	s.record(path, indexFile(path, nil))
}

// watchedFilesChanged keeps the index in step with files changed outside an
// open editor, and checks the open documents again.
func (s *Server) watchedFilesChanged(changes []FileEvent) {
	for _, c := range changes {
		if _, open := s.docs[c.URI]; open {
			continue
		}
		s.reindexFromDisk(pathOf(c.URI))
	}
	s.republish()
}

// publishedContext is what every global policy indexed publishes, in a
// stable order.
func (s *Server) publishedContext() []analysis.Published {
	paths := make([]string, 0, len(s.published))
	for p := range s.published {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]analysis.Published, 0, len(paths))
	for _, p := range paths {
		out = append(out, s.published[p])
	}
	return out
}

// publishedIn reads what a file publishes, from src, or from disk when src is
// nil. Files that cannot be a global policy are passed over unparsed.
func publishedIn(path string, src []byte) (analysis.Published, bool) {
	if src == nil {
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxIndexedSize {
			return analysis.Published{}, false
		}
		//nolint:gosec // reading the workspace's own files is the point
		if src, err = os.ReadFile(path); err != nil {
			return analysis.Published{}, false
		}
	}
	if !bytes.Contains(src, []byte("Application")) || !bytes.Contains(src, []byte("global")) {
		return analysis.Published{}, false
	}
	return analysis.PublishedContext(path, src)
}

// crdDocuments are the CustomResourceDefinitions in a YAML stream.
func crdDocuments(src []byte) [][]byte {
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(src)))
	var out [][]byte
	for {
		doc, err := r.Read()
		if err != nil {
			return out
		}
		if bytes.Contains(doc, []byte("kind: CustomResourceDefinition")) {
			out = append(out, doc)
		}
	}
}

const (
	validateAuto = "auto"
	validateOn   = "on"
	validateOff  = "off"
)

// clusterState is what reaching the kubeconfig's cluster found.
type clusterState struct {
	fetch   kubeschema.Fetch
	vela    bool
	context string
	err     error
}

// connectCluster reaches the kubeconfig's cluster once, on a goroutine, and
// hands what it found to the message loop: the kinds it serves, for checking
// outputs, and its Package resources.
func (s *Server) connectCluster() {
	if !s.clusterEnabled {
		s.notifyCluster()
		return
	}
	if s.cluster != nil || s.connect == nil {
		return
	}
	s.cluster = &clusterState{}
	connect := s.connect
	go func() {
		c, err := connect()
		s.post(func() {
			if !s.clusterEnabled {
				return
			}
			if s.useCluster(c, err) {
				s.republish()
			}
			s.notifyCluster()
		})
	}()
}

// readClusterNow reaches the cluster and keeps what it found, at once.
func (s *Server) readClusterNow(connect ClusterConnector) {
	c, err := connect()
	s.useCluster(c, err)
}

// useCluster keeps what reaching the cluster found: the kinds it serves,
// when it runs KubeVela, and its packages. It reports whether that changes
// what documents are checked against.
func (s *Server) useCluster(c Cluster, err error) bool {
	var pkgs []cuexruntime.Package
	for i := range c.Packages {
		if p, err := cuexruntime.NewExternalPackage(&c.Packages[i]); err == nil {
			pkgs = append(pkgs, p)
		}
	}
	s.cluster = &clusterState{fetch: c.Fetch, vela: c.KubeVela && err == nil, context: c.Context, err: err}
	s.clusterPackages = pkgs
	if len(pkgs) > 0 {
		s.rebuildExternals()
	}
	// Without KubeVela, the cluster adds no kinds to those already built.
	if s.cluster.vela {
		s.rebuildKinds()
	}
	return s.cluster.vela || len(pkgs) > 0
}

// rebuildKinds builds the schemas outputs are checked against: Kubernetes'
// own kinds, the workspace's CRDs, and, with a KubeVela cluster, the kinds it
// serves. They are nil when the setting turns checking off, or when it is
// auto and the kubeconfig reaches no KubeVela cluster.
func (s *Server) rebuildKinds() {
	vela := s.cluster != nil && s.cluster.vela
	if s.validateOutputs == validateOff || (s.validateOutputs != validateOn && !vela) {
		s.kinds = nil
		return
	}
	kinds, err := kubeschema.Builtin()
	if err != nil {
		s.kinds = nil
		return
	}
	paths := make([]string, 0, len(s.crds))
	for p := range s.crds {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, doc := range s.crds[p] {
			_ = kinds.AddCRD(doc)
		}
	}
	if vela && s.cluster.fetch != nil {
		kinds.SetFallback(s.cluster.fetch)
	}
	s.kinds = kinds
}

// dropCluster forgets what was read of the cluster, and checks the open
// documents again without it.
func (s *Server) dropCluster() {
	s.cluster = nil
	s.clusterPackages = nil
	s.rebuildExternals()
	s.rebuildKinds()
	s.republish()
	s.notifyCluster()
}

// reconnectCluster reads the cluster again.
func (s *Server) reconnectCluster() {
	s.cluster = nil
	s.clusterPackages = nil
	s.connectCluster()
}

// notifyCluster tells the client what the server reads of the cluster.
func (s *Server) notifyCluster() {
	if s.out == nil {
		return
	}
	status := ClusterStatus{Enabled: s.clusterEnabled}
	if s.clusterEnabled && s.cluster != nil {
		status.Context = s.cluster.context
		status.KubeVela = s.cluster.vela
		status.Reachable = s.cluster.err == nil
		status.Packages = len(s.clusterPackages)
		if s.cluster.err != nil {
			status.Error = s.cluster.err.Error()
		}
	}
	_ = s.write(message{JSONRPC: "2.0", Method: MethodClusterStatus, Params: mustJSON(status)})
}
