package qlcompiler

import (
	"context"
	"cuelang.org/go/cue"
	"github.com/kubevela/pkg/cue/cuex"
)

type Compiler interface {
	CompileString(ctx context.Context, src string) (cue.Value, error)
	CompileStringWithOptions(ctx context.Context, s string, opts ...cuex.CompileOption) (cue.Value, error)
}
