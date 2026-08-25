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

package cuex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A SourceDefinition is a cached, shared read. The packages it may import are
// therefore the ones that fetch, and not the ones that act.
//
// helm.#Render is the case that matters: outside dry-run it calls
// installOrUpgradeChart, so a source importing vela/helm would install a real
// release on every cache miss, from something whose contract is a read. The
// render path sets no dry-run, so nothing else would stop it.
func TestSourceCompilerRefusesActingPackages(t *testing.T) {
	for _, tc := range []struct{ name, pkg, expr string }{
		{"helm installs", "vela/helm", "output: x: helm.#Render.#do"},
		{"addon installs", "vela/addon", "output: x: addon.#Render.#do"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "import \"" + tc.pkg + "\"\n" + tc.expr + "\n"
			// An absent package is not a compile error: it surfaces when the
			// value is evaluated, which is what the resolver and the webhook
			// both do.
			v, err := SourceCompiler.Get().CompileString(context.Background(), src)
			require.NoError(t, err)
			require.Error(t, v.Err(), "a source must not be able to import %s", tc.pkg)
			require.Contains(t, v.Err().Error(), tc.pkg)
		})
	}

	// The same templates evaluate fine on WorkloadCompiler, which is what makes
	// this a restriction rather than a coincidence.
	for _, tc := range []struct{ pkg, expr string }{
		{"vela/helm", "output: x: helm.#Render.#do"},
		{"vela/addon", "output: x: addon.#Render.#do"},
	} {
		src := "import \"" + tc.pkg + "\"\n" + tc.expr + "\n"
		v, err := WorkloadCompiler.Get().CompileString(context.Background(), src)
		require.NoError(t, err)
		require.NoError(t, v.Err(), "%s must still work for components", tc.pkg)
	}
}

// The fetching packages a source is built on have to stay available, or the
// nine shipped definitions stop compiling.
func TestSourceCompilerKeepsFetchingPackages(t *testing.T) {
	for _, tc := range []struct{ pkg, use string }{
		{"vela/kube", "kube.#Get.#do"},
		{"vela/http", "http.#Do.#do"},
		{"vela/base64", "base64.#Decode.#do"},
		{"vela/registry", "registry.#ReadFile.#do"},
		{"vela/velaconfig", "velaconfig.#Read.#do"},
	} {
		t.Run(tc.pkg, func(t *testing.T) {
			v, err := SourceCompiler.Get().CompileString(context.Background(),
				"import \""+tc.pkg+"\"\noutput: x: "+tc.use+"\n")
			require.NoError(t, err)
			require.NoError(t, v.Err(), "%s must remain importable by a source", tc.pkg)
		})
	}
}

// Adding a package to WorkloadCompiler should not silently widen what a source
// can do. This fails when the two drift, which is the moment to decide whether
// the new package is a fetch or an action.
func TestSourceCompilerPackageSetIsDeliberate(t *testing.T) {
	require.ElementsMatch(t, []string{
		"base64", "cue", "http", "kube", "registry", "velaconfig",
	}, sourceCompilerPackageNames(),
		"the source package set changed; a source may fetch, not act")
}
