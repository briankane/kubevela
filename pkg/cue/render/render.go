package render

import (
	"bytes"
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/tools/fix"
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
	"k8s.io/klog"
	"regexp"
	"slices"
	"strings"
	"text/template"
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
	GetImports(ctx process.Context, abstractTmpl string) (string, error)
	PreRender(ctx process.Context, abstractTmpl string) (cue.Value, error)
	GetContext(ctx process.Context, tmplCue cue.Value) (string, error)
	GetConfiguration(ctx process.Context, tmplCue cue.Value) (string, error)
	GetParameterTemplate(ctx process.Context, tmplCue cue.Value) (string, error)
	GetParameters(ctx process.Context, tmplCue cue.Value, params map[string]interface{}) (string, error)
	GetFields(ctx process.Context, tmplCue cue.Value) (string, error)
	GetOutput(ctx process.Context, tmplCue cue.Value) (string, error)
	GetOutputs(ctx process.Context, tmplCue cue.Value) (string, error)
}

func Render(re renderEngine, ctx process.Context, abstractTmpl string, params map[string]interface{}) (RenderedTemplate, error) {
	imports, err := re.GetImports(ctx, abstractTmpl)
	if err != nil {
		return "", err
	}
	render, err := re.PreRender(ctx, abstractTmpl)
	if err != nil {
		return "", err
	}

	context, _ := re.GetContext(ctx, render)
	config, _ := re.GetConfiguration(ctx, render)
	parameterTemplate, _ := re.GetParameterTemplate(ctx, render)
	parameters, _ := re.GetParameters(ctx, render, params)
	fields, _ := re.GetFields(ctx, render)
	output, _ := re.GetOutput(ctx, render)
	outputs, _ := re.GetOutputs(ctx, render)

	data := struct {
		Imports           string
		Context           string
		Config            string
		ParameterTemplate string
		Parameters        string
		Fields            string
		Output            string
		Outputs           string
	}{
		Imports:           imports,
		Context:           context,
		Config:            config,
		ParameterTemplate: parameterTemplate,
		Parameters:        parameters,
		Fields:            fields,
		Output:            output,
		Outputs:           outputs,
	}

	var tmpl = strings.TrimSpace(dedent.Dedent(`
		{{- if .Imports }}
		// Imports
		{{.Imports}}

		{{- end}}
		// Context Definition
		context: [string]: _

		// Context Values
		context: {{.Context}}

		// Configuration Definition
		config: [string]: _

		// Configuration Values
		config: {{.Config}}

		// Parameter Definition
		parameter: {{.ParameterTemplate}}

		// Parameter Values
		parameter: {{.Parameters}}

		{{- if .Fields }}

		// Fields
		{{ .Fields}}

		{{- end }}

		// Output
		output: {{.Output}}

		// Outputs
		outputs: {{.Outputs}}
	`))

	t, err := template.New("render").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	str := regexp.MustCompile(`\n{3,}`).ReplaceAllString(buf.String(), "\n\n")
	str = strings.TrimSpace(str)

	return RenderedTemplate(str), nil
}

type ComponentRenderEngine struct{}

type TraitRenderEngine struct{}

func (re ComponentRenderEngine) GetImports(ctx process.Context, abstractTmpl string) (string, error) {
	var packageImports []string
	for _, i := range cuex.DefaultCompiler.Get().GetImports() {
		packageImports = append(packageImports, i.ImportPath)
	}

	var imports []string
	file, err := parser.ParseFile("input.cue", abstractTmpl, parser.ImportsOnly)
	if err != nil {
		fmt.Printf("failed to parse abstract template: %v\n", err)
		return "", err
	}
	n := fix.File(file)
	for _, decl := range n.Decls {
		if importDecl, ok := decl.(*ast.ImportDecl); ok {
			imports = append(imports, importDecl.Specs[0].Path.Value)
		}
	}
	if len(imports) > 0 {
		var lines []string
		for _, imp := range imports {
			if !slices.Contains(packageImports, imp) {
				return "", errors.New("package %s is not imported into compiler")
			}
			lines = append(lines, fmt.Sprintf("    %s", imp))
		}
		return "import (\n" + strings.Join(lines, ",\n") + "\n)", nil
	}
	return "", nil
}

func (re ComponentRenderEngine) PreRender(ctx process.Context, abstractTmpl string) (cue.Value, error) {
	baseCtx, err := ctx.BaseContextFile()
	if err != nil {
		klog.Errorf("failed to load base context file\n%v", err)
		return cue.Value{}, err
	}
	val, err := cuex.DefaultCompiler.Get().CompileString(ctx.GetCtx(), abstractTmpl+"\n"+baseCtx)
	if err != nil {
		return cue.Value{}, err
	}
	return val, nil
}

func (re ComponentRenderEngine) GetContext(ctx process.Context, tmplCue cue.Value) (string, error) {
	ctxField := tmplCue.LookupPath(cue.ParsePath("context"))
	if !ctxField.Exists() {
		return "{}", nil
	}
	syntax := ctxField.Syntax(cue.Raw())
	b, err := format.Node(syntax)
	if err != nil {
		klog.Errorf("failed to load context! \n%v", err)
	}
	return string(b), nil
}

func (re ComponentRenderEngine) GetConfiguration(ctx process.Context, tmplCue cue.Value) (string, error) {
	var configMap = make(map[string]interface{})
	configField := tmplCue.LookupPath(cue.ParsePath("config"))
	if configField.Exists() {
		iter, err := configField.Fields()
		if err != nil {
			klog.Errorf("couldn't load config fields\n%s", err)
			return "", err
		}
		for iter.Next() {
			configKey := iter.Label()
			config := iter.Value()
			content, err := getConfigFromCueVal(ctx, configKey, config)
			if err != nil {
				klog.Errorf("couldn't load config `config.%s`\n%s", configKey, err)
				return "", err
			}
			configMap[configKey] = content
		}
	}
	cueStr, err := json.Marshal(configMap)
	if err != nil {
		klog.Errorf("failed to marshal field `config`\n%v", err)
		return "", err
	}
	cueVal := cuecontext.New().CompileString(string(cueStr))
	syntax := cueVal.Syntax(cue.Final())
	b, err := format.Node(syntax)
	return string(b), err
}

func (re ComponentRenderEngine) GetParameterTemplate(ctx process.Context, tmplCue cue.Value) (string, error) {
	paramField := tmplCue.LookupPath(cue.ParsePath("parameter"))
	if !paramField.Exists() {
		return "{}", nil
	}
	syntax := paramField.Syntax(cue.Raw())
	b, err := format.Node(syntax)
	return string(b), err
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
		b, err := format.Node(syntax)
		return string(b), err
	}
	return "{}", nil
}

func (re ComponentRenderEngine) GetFields(ctx process.Context, tmplCue cue.Value) (string, error) {
	output := ""
	iter, _ := tmplCue.Fields()
	for iter.Next() {
		fieldName := iter.Selector().String()
		if !slices.Contains(reserved, fieldName) {
			syntax := iter.Value().Syntax(cue.Final())
			b, _ := format.Node(syntax)
			output = output + fmt.Sprintf("%s: %s\n\n", fieldName, string(b))
		}
	}
	return output, nil
}

func (re ComponentRenderEngine) GetOutput(ctx process.Context, tmplCue cue.Value) (string, error) {
	outputField := tmplCue.LookupPath(cue.ParsePath("output"))
	if !outputField.Exists() {
		return "{}", nil
	}
	syntax := outputField.Syntax(cue.Raw())
	b, err := format.Node(syntax)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (re ComponentRenderEngine) GetOutputs(ctx process.Context, tmplCue cue.Value) (string, error) {
	outputsField := tmplCue.LookupPath(cue.ParsePath("outputs"))
	if !outputsField.Exists() {
		return "{}", nil
	}
	syntax := outputsField.Syntax(cue.Raw())
	b, _ := format.Node(syntax)
	return string(b), nil
}

func getConfigFromCueVal(ctx process.Context, key string, config cue.Value) (map[string]interface{}, error) {
	cfgName := config.LookupPath(value.FieldPath("name"))
	if !cfgName.Exists() {
		return nil, errors.New(
			fmt.Sprintf("Invalid configuration provided in field `config.%s`. Must specify `name` field.", key))
	}
	cfgNameStr, _ := cfgName.String()
	cfgNamespace := config.LookupPath(value.FieldPath("namespace"))
	cfgNamespaceStr := oam.SystemDefinitionNamespace
	if cfgNamespace.Exists() {
		ns, err := cfgNamespace.String()
		if err != nil {
			klog.Errorf("invalid string value supplied for `config.%s.name`\n%v", key, err)
			return nil, err
		}
		cfgNamespaceStr = ns
	}
	return common.ReadConfig(ctx.GetCtx(), singleton.KubeClient.Get(), cfgNamespaceStr, cfgNameStr)
}
