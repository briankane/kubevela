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
	"github.com/kubevela/pkg/cue/cuex"
	"github.com/spf13/cobra"

	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/definition/lsp"
)

// NewDefinitionLSPCommand runs the language server for CUE definitions.
func NewDefinitionLSPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lsp",
		Short: "Run a language server for CUE definitions.",
		Long: "Run a Language Server Protocol server for CUE X-Definitions over stdin and stdout.\n" +
			"Editors start it themselves. It reports the errors the controller would hit compiling a\n" +
			"definition, at the line they are on. It works offline: it reads the files the editor sends\n" +
			"and the vela/* packages built into this binary, never the cluster.",
		Example: "# Configure your editor to start:\n" +
			"> vela def lsp",
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			types.TagCommandType:  types.TypeDefManagement,
			types.TagCommandOrder: "9",
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			// An editor must not quietly read Package CRs from whichever
			// cluster the kubeconfig points at.
			cuex.EnableExternalPackageForDefaultCompiler = false
			cuex.EnableExternalPackageWatchForDefaultCompiler = false
			return lsp.NewServer().Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	// Language clients pass --stdio when told to use that transport; it is
	// the only one, so the flag is accepted and has no effect.
	cmd.Flags().Bool("stdio", true, "Talk over stdin and stdout (the only transport).")
	return cmd
}
