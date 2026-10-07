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

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kubevela/pkg/cue/cuex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/definition/lsp"
)

// NewDefinitionLintCommand checks definitions and the files around them as
// the language server does.
func NewDefinitionLintCommand() *cobra.Command {
	var (
		asJSON, quiet, verbose, kinds, cluster bool
		failOn                                 string
		workspace                              []string
	)
	cmd := &cobra.Command{
		Use:   "lint [PATH...]",
		Short: "Check CUE definitions, their tests, addons and Package resources.",
		Long: "Check every CUE and YAML file under the paths given (the current directory by default) as\n" +
			"the editor does, with the same index of the files around each: definitions, including those\n" +
			"a definition extends; CUE test files; addons; Package resources. Errors are what KubeVela\n" +
			"would reject or fail on; warnings are what it would ignore or work around.\n\n" +
			"The files around each are those of the workspace, as the folder opened in an editor: the\n" +
			"current directory, or the path's own folder for a path outside it, or --workspace. So a\n" +
			"file checked alone still finds the definition it extends and the packages it imports.\n\n" +
			"It prints one line per finding and a summary, and exits non-zero when it finds anything at\n" +
			"or above --fail-on.",
		Example: "# Check the definitions in this directory\n" +
			"> vela def lint\n\n" +
			"# Check a module and an addon, failing on warnings too\n" +
			"> vela def lint ./definitions ./addons/my-addon --fail-on warning\n\n" +
			"# Check one file, against the packages and definitions of the repository it is in\n" +
			"> vela def lint definitions/my-trait.cue --workspace .\n\n" +
			"# Also check against the kinds and Package resources of the kubeconfig's cluster\n" +
			"> vela def lint --cluster",
		Annotations: map[string]string{
			types.TagCommandType:  types.TypeDefManagement,
			types.TagCommandOrder: "10",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			threshold, ok := map[string]int{lsp.FindingError: 0, lsp.FindingWarning: 1, lsp.FindingInfo: 2}[failOn]
			if !ok {
				return errors.Errorf("--fail-on takes %s, %s or %s", lsp.FindingError, lsp.FindingWarning, lsp.FindingInfo)
			}
			if len(args) == 0 {
				args = []string{"."}
			}
			// Packages come from the files, or from --cluster: the compiler
			// must not load them from the kubeconfig's cluster on its own.
			cuex.EnableExternalPackageForDefaultCompiler = false
			cuex.EnableExternalPackageWatchForDefaultCompiler = false
			if len(workspace) == 0 {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				workspace = lintWorkspace(args, cwd, isDir)
			}
			opts := lsp.CheckOptions{Kinds: kinds, Workspace: workspace}
			if cluster {
				opts.Cluster = lsp.ConnectKubeconfig
			}
			findings, files, err := lsp.Check(args, opts)
			if err != nil {
				return err
			}
			rank := map[string]int{lsp.FindingError: 0, lsp.FindingWarning: 1, lsp.FindingInfo: 2}
			counts := map[string]int{}
			var shown []lsp.Finding
			failed := 0
			for _, f := range findings {
				counts[f.Severity]++
				if rank[f.Severity] <= threshold {
					failed++
				}
				if f.Severity != lsp.FindingInfo || verbose {
					shown = append(shown, f)
				}
			}
			out := cmd.OutOrStdout()
			switch {
			case asJSON:
				if shown == nil {
					shown = []lsp.Finding{}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(map[string]interface{}{"files": files, "findings": shown, "errors": counts[lsp.FindingError], "warnings": counts[lsp.FindingWarning], "info": counts[lsp.FindingInfo]}); err != nil {
					return err
				}
			default:
				if !quiet {
					for _, f := range shown {
						fmt.Fprintf(out, "%s:%d:%d: %s: %s\n", relative(f.Path), f.Line, f.Column, f.Severity, f.Message)
					}
				}
				fmt.Fprintf(out, "%d files: %d errors, %d warnings, %d info\n", files, counts[lsp.FindingError], counts[lsp.FindingWarning], counts[lsp.FindingInfo])
			}
			if failed > 0 {
				cmd.SilenceUsage = true
				return errors.Errorf("%d findings at or above %s", failed, failOn)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the findings as JSON.")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Print the summary only.")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print information findings too, such as missing +usage markers.")
	cmd.Flags().BoolVar(&kinds, "kinds", true, "Check outputs against Kubernetes' own kinds and the CRDs among the files.")
	cmd.Flags().BoolVar(&cluster, "cluster", false, "Also read the kubeconfig's cluster, read-only: the kinds it serves, when it runs KubeVela, and its Package resources.")
	cmd.Flags().StringSliceVar(&workspace, "workspace", nil, "The folders the files around those checked come from: definitions they extend, Package resources and CRDs. Defaults to the current directory, or a path's own folder for a path outside it.")
	cmd.Flags().StringVar(&failOn, "fail-on", lsp.FindingError, "Exit non-zero on findings of this severity or worse: error, warning or info.")
	return cmd
}

// relative is path relative to the working directory, when it is under it.
// lintWorkspace is the workspace of the paths linted, from the current
// directory cwd: cwd for a path under it, as nothing given is; for a path
// elsewhere, the folder it is or is in.
func lintWorkspace(paths []string, cwd string, isDir func(string) bool) []string {
	if len(paths) == 0 {
		return []string{cwd}
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		root := cwd
		if rel, err := filepath.Rel(cwd, p); err != nil || rel == ".." || hasDotDot(rel) {
			root = p
			if !isDir(p) {
				root = filepath.Dir(p)
			}
		}
		if !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	sort.Strings(out)
	return out
}

// isDir reports whether path is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func relative(path string) string {
	if wd, err := filepath.Abs("."); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && !filepath.IsAbs(rel) && rel != ".." && !hasDotDot(rel) {
			return rel
		}
	}
	return path
}

func hasDotDot(rel string) bool {
	return len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}
