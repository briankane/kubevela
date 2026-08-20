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

package celexpr

import (
	"sync"

	"github.com/google/cel-go/cel"
	"k8s.io/utils/lru"
)

// The render path evaluates every property of every component and every trait on
// every reconcile, and almost all of that work was being thrown away and redone:
//
//	building the environment      19,819 ns   a constant
//	compiling + building the program 35,893 ns   a function of the expression text
//	evaluating                         644 ns   the actual work
//
// Measured end to end, one expression cost 86,407 ns, of which 653 ns survives
// once both are kept. Nothing about either input varies between calls - the
// permissive environment is two variable declarations and the standard
// libraries, and an expression's compilation depends only on its text and the
// environment it was compiled against.
const programCacheSize = 2048

var (
	dynEnvOnce sync.Once
	dynEnvVal  *cel.Env
	dynEnvErr  error

	// Keyed on expression text alone, which is only sound because entries are
	// stored for exactly one environment - the shared permissive one. See
	// compiledFor.
	//
	// lru.Cache locks internally, so this carries no mutex of its own.
	compiledCache = lru.New(programCacheSize)
)

// compiled is one expression's compilation: the AST that References walks, and
// the program Eval runs. Both are read-only once built and safe to share.
type compiled struct {
	ast *cel.Ast
	prg cel.Program
}

// compiledFor returns expr compiled against env, reusing an earlier compilation
// when it can.
//
// Only compilations against the shared permissive environment are cached, and
// the identity check is what keeps the cache honest. A typed environment from
// EnvForContext declares each binding's real shape, so the same text compiles to
// a different result there - and returning a dyn-typed program to a caller that
// asked for a typed one would silently disable the target-type check that stops
// a string reaching an int parameter. Every other environment therefore compiles
// fresh, exactly as before.
//
// That costs nothing worth having: those environments belong to admission, which
// is neither hot nor repeated, while the render path uses the shared one.
//
// Failures are not cached. They are the error path, they are rare, and caching
// them would mean holding an error whose text a future caller might reasonably
// expect to reflect their own call.
func compiledFor(env *cel.Env, expr string) (*compiled, error) {
	shared, sharedErr := DynEnv()
	cacheable := sharedErr == nil && env == shared

	if cacheable {
		if hit, ok := compiledCache.Get(expr); ok {
			if c, ok := hit.(*compiled); ok {
				return c, nil
			}
		}
	}

	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	c := &compiled{ast: ast, prg: prg}

	if cacheable {
		compiledCache.Add(expr, c)
	}
	return c, nil
}
