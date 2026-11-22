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

package sources

import (
	"context"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	upstreamcuex "github.com/kubevela/pkg/cue/cuex"
	"github.com/stretchr/testify/require"
)

// ownerCompiler records the owner each resolving compile ran with, by the
// source's output value.
type ownerCompiler struct {
	SourceCompiler
	seen map[string]string
}

func (c *ownerCompiler) CompileString(ctx context.Context, src string) (cue.Value, error) {
	owner := "none"
	if o, ok := upstreamcuex.OwnerFrom(ctx); ok {
		owner = o.Kind + " " + o.Namespace + "/" + o.Name
	}
	for _, tag := range []string{"from-team", "from-nowhere"} {
		if strings.Contains(src, tag) {
			c.seen[tag] = owner
		}
	}
	return c.SourceCompiler.CompileString(ctx, src)
}

// A source compiles as its own SourceDefinition, not as the component whose
// render it resolves inside; one with no known definition compiles as none.
func TestSourceCompilesAsItsDefinition(t *testing.T) {
	r := require.New(t)
	compiler := &ownerCompiler{SourceCompiler: upstreamcuex.NewCompilerWithInternalPackages(), seen: map[string]string{}}
	opts := demoEngineOptions()
	opts.Compiler = compiler
	opts.Bindings = map[string]map[string]interface{}{"team": {}, "loose": {}}
	opts.Types = map[string]string{"team": "team-cfg", "loose": "loose-cfg"}
	opts.Templates = map[string]string{
		"team-cfg":  "schema: {v: string}\n$internal: {key: \"t\", keyInputs: []}\noutput: {v: \"from-team\"}\n",
		"loose-cfg": "schema: {v: string}\n$internal: {key: \"l\", keyInputs: []}\noutput: {v: \"from-nowhere\"}\n",
	}
	opts.Owners = map[string]*upstreamcuex.Owner{"team-cfg": {Kind: "SourceDefinition", Namespace: "team-a", Name: "team-cfg"}}
	engine, err := NewSourceEngine(opts)
	r.NoError(err)

	ctx := upstreamcuex.WithOwner(context.Background(), upstreamcuex.Owner{Kind: "ComponentDefinition", Namespace: "vela-system", Name: "webservice"})
	_, err = engine.Resolve(ctx, map[string]interface{}{"a": "$(source.team.v)", "b": "$(source.loose.v)"})
	r.NoError(err)
	r.Equal(map[string]string{"from-team": "SourceDefinition team-a/team-cfg", "from-nowhere": "none"}, compiler.seen)
}
