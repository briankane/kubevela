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
	"os"
	"path/filepath"
)

// Finding is what a check found in a file, for a command line: its line
// and column, from 1.
type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Severities, as a Finding names them.
const (
	FindingError   = "error"
	FindingWarning = "warning"
	FindingInfo    = "info"
)

// CheckOptions say what a check draws on beyond the files.
type CheckOptions struct {
	// Kinds checks outputs against Kubernetes' own kinds and the CRDs among
	// the files.
	Kinds bool
	// Cluster, when set, is read for its Package resources and, with Kinds,
	// the kinds it serves.
	Cluster ClusterConnector
}

// Check checks every CUE and YAML file under roots as the language server
// checks an open one, with the same index of the files around it. It
// returns what it found, in file order, and how many files it read.
func Check(roots []string, opts CheckOptions) ([]Finding, int, error) {
	folders := make([]string, 0, len(roots))
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, 0, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, 0, err
		}
		folders = append(folders, abs)
	}
	s := NewServer(WithCluster(opts.Cluster))
	s.folders = folders
	s.validateOutputs = validateOff
	if opts.Kinds {
		s.validateOutputs = validateOn
	}
	found, files := walkWorkspace(folders)
	for path, entry := range found {
		if entry.contributes() {
			s.record(path, entry)
		}
	}
	if opts.Cluster != nil {
		s.readCluster(opts.Cluster)
	}
	s.rebuildKinds()
	var findings []Finding
	for _, path := range files {
		//nolint:gosec // reading the files asked for is the point
		text, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		for _, d := range s.diagnose("file://"+path, string(text)) {
			findings = append(findings, Finding{
				Path:     path,
				Line:     int(d.Range.Start.Line) + 1,
				Column:   int(d.Range.Start.Character) + 1,
				Severity: findingSeverity(d.Severity),
				Message:  d.Message,
			})
		}
	}
	return findings, len(files), nil
}

func findingSeverity(s DiagnosticSeverity) string {
	switch s {
	case SeverityError:
		return FindingError
	case SeverityWarning:
		return FindingWarning
	case SeverityInformation:
		return FindingInfo
	}
	return FindingInfo
}
