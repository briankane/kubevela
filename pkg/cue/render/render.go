package render

import (
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"encoding/json"
	"fmt"
	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/pkg/util/singleton"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/process"
	"github.com/lithammer/dedent"
	"github.com/oam-dev/kubevela/pkg/config/common"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/pkg/errors"
	"slices"
	"strings"
)

var reserved = []string{
	model.OutputFieldName,
	model.OutputsFieldName,
	model.ParameterFieldName,
	model.ContextFieldName,
	"config",
}

var ComponentEngine = newRenderer[ComponentRenderEngine]()

type Renderer struct {
	engine renderEngine
}

func newRenderer[T renderEngine]() Renderer {
	return Renderer{engine: *new(T)}
}

func (r *Renderer) Render(ctx process.Context, abstractTmpl string, params interface{}) (RenderedTemplate, error) {
	return Render(r.engine, ctx, abstractTmpl, params.(map[string]interface{}))
}

type RenderedTemplate string

func (rt RenderedTemplate) StrValue() string {
	return string(rt)
}

func (rt RenderedTemplate) Compile(ctx process.Context) (cue.Value, error) {
	return cuex.DefaultCompiler.Get().CompileString(ctx.GetCtx(), string(rt))
}

type renderEngine interface {
	PreRender(ctx process.Context, abstractTmpl string) (cue.Value, error)
	GetContext(ctx process.Context, preRender cue.Value) (string, error)
	GetConfiguration(ctx process.Context, preRender cue.Value) (string, error)
	GetParameterTemplate(ctx process.Context, preRender cue.Value) (string, error)
	GetParameters(ctx process.Context, preRender cue.Value, params map[string]interface{}) (string, error)
	GetFields(ctx process.Context, preRender cue.Value) (string, error)
	GetOutput(ctx process.Context, preRender cue.Value) (string, error)
	GetOutputs(ctx process.Context, preRender cue.Value) (string, error)
}

func Render(re renderEngine, ctx process.Context, abstractTmpl string, params map[string]interface{}) (RenderedTemplate, error) {
	render, _ := re.PreRender(ctx, abstractTmpl)
	context, _ := re.GetContext(ctx, render)

	config, _ := re.GetConfiguration(ctx, render)
	parameterTemplate, _ := re.GetParameterTemplate(ctx, render)
	parameters, _ := re.GetParameters(ctx, render, params)
	fields, _ := re.GetFields(ctx, render)
	output, _ := re.GetOutput(ctx, render)
	outputs, _ := re.GetOutputs(ctx, render)

	return RenderedTemplate(
		fmt.Sprintf(
			strings.TrimSpace(
				dedent.Dedent(`
					// Context Definition
					context: [string]: _

					// Context Values
					context: %s

					// Configuration Definition
					config: [string]: _

					// Configuration Values
					config: %s

					// Parameter Definition
					parameter: %s

					// Parameter Values
					parameter: %s

					// Fields
					%s

					// Output
					output: %s

					// Outputs (Ancillary)
					outputs: %s
				`),
			),
			context,
			config,
			parameterTemplate,
			parameters,
			fields,
			output,
			outputs,
		),
	), nil
}

type ComponentRenderEngine struct{}

type TraitRenderEngine struct{}

func (re ComponentRenderEngine) PreRender(ctx process.Context, abstractTmpl string) (cue.Value, error) {
	baseCtx, _ := ctx.BaseContextFile()
	val := cuecontext.New().CompileString(abstractTmpl + "\n" + baseCtx)
	return val, nil
}

func (re ComponentRenderEngine) GetContext(ctx process.Context, preRender cue.Value) (string, error) {
	ctxField := preRender.LookupPath(cue.ParsePath("context"))
	if !ctxField.Exists() {
		return "{}", nil
	}
	syntax := ctxField.Syntax(cue.Raw())
	b, _ := format.Node(syntax)
	return string(b), nil
}

func (re ComponentRenderEngine) GetConfiguration(ctx process.Context, preRender cue.Value) (string, error) {
	var configMap = make(map[string]interface{})
	configField := preRender.LookupPath(cue.ParsePath("config"))
	if configField.Exists() {
		iter, err := configField.Fields()
		if err != nil {
			panic(err)
		}

		for iter.Next() {
			configKey := iter.Label()
			config := iter.Value()

			cfgName := config.LookupPath(value.FieldPath("name"))
			if !cfgName.Exists() {
				continue
			}
			cfgNameStr, _ := cfgName.String()

			cfgNamespace := config.LookupPath(value.FieldPath("namespace"))
			cfgNamespaceStr := oam.SystemDefinitionNamespace
			if cfgNamespace.Exists() {
				cfgNamespaceStr, err = cfgNamespace.String()
			}

			content, _ := common.ReadConfig(ctx.GetCtx(), singleton.KubeClient.Get(), cfgNamespaceStr, cfgNameStr)
			configMap[configKey] = content
		}
	}
	cueStr, _ := json.Marshal(configMap)
	cueVal := cuecontext.New().CompileString(string(cueStr))
	syntax := cueVal.Syntax(cue.Final())
	b, _ := format.Node(syntax)
	return string(b), nil
}

func (re ComponentRenderEngine) GetParameterTemplate(ctx process.Context, preRender cue.Value) (string, error) {
	paramField := preRender.LookupPath(cue.ParsePath("parameter"))
	if !paramField.Exists() {
		return "{}", nil
	}
	syntax := paramField.Syntax(cue.Raw())
	b, _ := format.Node(syntax)
	return string(b), nil
}

func (re ComponentRenderEngine) GetParameters(ctx process.Context, _ cue.Value, params map[string]interface{}) (string, error) {
	name := ctx.GetData(model.ContextName)
	if params != nil && len(params) > 0 {
		bt, err := json.Marshal(params)
		if err != nil {
			return "", errors.WithMessagef(err, "marshal parameter of workload %s", name)
		}
		cueStr := string(bt)
		cueVal := cuecontext.New().CompileString(cueStr)
		syntax := cueVal.Syntax(cue.Raw())
		b, _ := format.Node(syntax)
		return string(b), nil
	}
	return "{}", nil
}

func (re ComponentRenderEngine) GetFields(ctx process.Context, preRender cue.Value) (string, error) {
	output := ""
	iter, _ := preRender.Fields()
	for iter.Next() {
		fieldName := iter.Selector().String()
		if !slices.Contains(reserved, fieldName) {
			syntax := iter.Value().Syntax(cue.Raw())
			b, _ := format.Node(syntax)
			output = output + fmt.Sprintf("%s: %s\n", fieldName, string(b))
		}
	}
	return output, nil
}

func (re ComponentRenderEngine) GetOutput(ctx process.Context, preRender cue.Value) (string, error) {
	outputField := preRender.LookupPath(cue.ParsePath("output"))
	if !outputField.Exists() {
		return "{}", nil
	}
	syntax := outputField.Syntax(cue.Raw())
	b, _ := format.Node(syntax)
	return string(b), nil
}

func (re ComponentRenderEngine) GetOutputs(ctx process.Context, preRender cue.Value) (string, error) {
	outputsField := preRender.LookupPath(cue.ParsePath("outputs"))
	if !outputsField.Exists() {
		return "{}", nil
	}
	syntax := outputsField.Syntax(cue.Raw())
	b, _ := format.Node(syntax)
	return string(b), nil
}
