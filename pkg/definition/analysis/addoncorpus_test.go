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
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAddonCorpus checks every file of every addon under
// VELA_ADDON_CORPUS, a checkout of kubevela/catalog's addons, and reports
// each error the checks find. VELA_ADDON_CORPUS_KNOWN names, one per line,
// "<path relative to the corpus>:<line>" errors already known to be the
// addon's own.
func TestAddonCorpus(t *testing.T) {
	root := os.Getenv("VELA_ADDON_CORPUS")
	if root == "" {
		t.Skip("VELA_ADDON_CORPUS is not set")
	}
	known := map[string]bool{}
	if f := os.Getenv("VELA_ADDON_CORPUS_KNOWN"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(data), "\n") {
			known[strings.TrimSpace(strings.SplitN(l, "#", 2)[0])] = true
		}
	}
	files, failed := 0, 0
	_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		diags, ok := CheckAddonFile(path, src, Options{})
		if !ok {
			if filepath.Ext(path) != cueExt || !strings.Contains(path, "/definitions/") {
				return nil
			}
			diags = Analyze(path, src).Diagnostics
		}
		files++
		rel, _ := filepath.Rel(root, path)
		for _, d := range diags {
			if d.Severity != SeverityError {
				continue
			}
			key := rel + ":" + strconv.Itoa(d.Range.Start.Line)
			if known[key] {
				continue
			}
			failed++
			t.Errorf("%s: %s", key, d.Message)
		}
		return nil
	})
	t.Logf("%d files, %d errors", files, failed)
}
