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
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
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

// indexWorkspace reads every CUE file in the workspace for what it publishes,
// on a goroutine, and hands the result to the message loop.
func (s *Server) indexWorkspace() {
	folders := append([]string{}, s.folders...)
	go func() {
		found := map[string]analysis.Published{}
		n := 0
		for _, root := range folders {
			_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
				if err != nil || n >= maxIndexedFiles {
					return filepath.SkipDir
				}
				if e.IsDir() {
					if path != root && (skippedDirs[e.Name()] || strings.HasPrefix(e.Name(), ".")) {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".cue") || utils.IsCUETestFile(path) {
					return nil
				}
				n++
				if p, ok := publishedIn(path, nil); ok {
					found[path] = p
				}
				return nil
			})
		}
		s.post(func() {
			for path, p := range found {
				if _, open := s.docs["file://"+path]; !open {
					s.published[path] = p
				}
			}
		})
	}()
}

// index records what a file publishes, from its text.
func (s *Server) index(path string, src []byte) {
	if p, ok := publishedIn(path, src); ok {
		s.published[path] = p
	} else {
		delete(s.published, path)
	}
}

// reindexFromDisk records what a file publishes as saved, or forgets it.
func (s *Server) reindexFromDisk(path string) {
	if p, ok := publishedIn(path, nil); ok {
		s.published[path] = p
	} else {
		delete(s.published, path)
	}
}

// watchedFilesChanged keeps the index in step with files changed outside an
// open editor.
func (s *Server) watchedFilesChanged(changes []FileEvent) {
	for _, c := range changes {
		if _, open := s.docs[c.URI]; open {
			continue
		}
		s.reindexFromDisk(pathOf(c.URI))
	}
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
