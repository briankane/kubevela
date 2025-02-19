package external

import (
	"context"
	"time"

	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"

	"github.com/kubevela/pkg/cue/cuex"

	"cuelang.org/go/cue"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"k8s.io/klog/v2"

	"github.com/oam-dev/kubevela/pkg/builtin"
	"github.com/oam-dev/kubevela/pkg/builtin/registry"
)

func init() {
	registry.RegisterRunner("external", newCmd)
}

type Cmd struct{}

func (c Cmd) Run(meta *registry.Meta) (results interface{}, err error) {
	bi := build.NewContext().NewInstance("", nil)
	val := cuecontext.New().BuildInstance(bi)

	do, _ := meta.Obj.LookupPath(value.FieldPath("#do")).String()
	provider, _ := meta.Obj.LookupPath(value.FieldPath("#provider")).String()
	params, _ := meta.Obj.LookupPath(value.FieldPath("$params")).Fields()
	val = val.FillPath(value.FieldPath("#do"), do)
	val = val.FillPath(value.FieldPath("#provider"), provider)
	val = val.FillPath(value.FieldPath("$params"), params)

	klog.Infof("Running external function %s::%s", provider, do)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cuex.DefaultCompiler.Get().Resolve(ctx, val)
}

func newCmd(_ cue.Value) (registry.Runner, error) {
	return &Cmd{}, nil
}

func Process(val cue.Value) (cue.Value, error) {
	external := val.LookupPath(value.FieldPath("external"))
	fields, _ := external.Fields()
	for {
		if !fields.Next() {
			break
		}
		externalObj := fields.Value()

		_, _ = exec(externalObj)
	}
	return val, nil
}

func exec(v cue.Value) (cue.Value, error) {
	resp, _ := builtin.RunTaskByKey("external", cue.Value{}, &registry.Meta{Obj: v})
	return resp.(cue.Value), nil
}
