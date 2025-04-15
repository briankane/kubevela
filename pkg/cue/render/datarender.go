package render

import (
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"encoding/json"
	"fmt"
	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/pkg/util/singleton"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/config/common"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"slices"
	"strings"
)

type ComponentDataRenderer struct{}

type dataRenderer interface {
	Render(ctx Ctx, file *ast.File, params interface{}) (*RenderedData, error)
	getImports(ctx Ctx, file *ast.File) (string, error)
	getContext(ctx Ctx, file *ast.File) (string, error)
	getParameterSpec(ctx Ctx, file *ast.File) (string, error)
	getParameters(ctx Ctx, file *ast.File, params interface{}) (string, error)
	getConfiguration(ctx Ctx, file *ast.File) (string, error)
	getData(ctx Ctx, file *ast.File) (string, error)
}

func (re ComponentDataRenderer) Render(ctx Ctx, file *ast.File, params interface{}) (*RenderedData, error) {
	tmpl := strings.Builder{}

	imports, _ := re.getImports(ctx, file)

	tmpl.WriteString(imports)

	tmpl.WriteString("\n\n")

	context, _ := re.getContext(ctx, file)
	tmpl.WriteString("context: " + context)

	tmpl.WriteString("\n\n")

	parameterSpec, _ := re.getParameterSpec(ctx, file)
	tmpl.WriteString("parameter: " + parameterSpec)

	tmpl.WriteString("\n\n")

	parameters, _ := re.getParameters(ctx, file, params)
	tmpl.WriteString("parameter: " + parameters)

	tmpl.WriteString("\n\n")

	configuration, _ := re.getConfiguration(ctx, file)
	tmpl.WriteString("$config: " + configuration)

	tmpl.WriteString("\n\n")

	data, _ := re.getData(ctx, file)
	tmpl.WriteString("$data: " + data)

	cueVal, _ := cuex.DefaultCompiler.Get().CompileString(ctx.ProcessCtx.GetCtx(), tmpl.String())

	config := cueVal.LookupPath(value.FieldPath("$config"))
	if config.Exists() {
		fields, _ := config.Fields()
		for fields.Next() {
			configKey := fields.Label()
			configVal := fields.Value()

			val, _ := getConfigFromCueVal(ctx, configKey, configVal)
			cueVal = cueVal.FillPath(value.FieldPath("$config."+configKey+".output"), val)
		}
	}
	return &RenderedData{
		Template: reconstituteTemplate(imports, cueVal),
		Cue:      cueVal,
	}, nil
}

func (re ComponentDataRenderer) getImports(ctx Ctx, file *ast.File) (string, error) {
	var packageImports []string
	for _, i := range cuex.DefaultCompiler.Get().GetImports() {
		packageImports = append(packageImports, i.ImportPath)
	}

	if len(file.Imports) > 0 {
		var lines []string
		for _, spec := range file.Imports {
			val := strings.Trim(spec.Path.Value, "\"")
			if !slices.Contains(packageImports, val) {
				return "", errors.New("package %s is not imported into compiler")
			}
			lines = append(lines, fmt.Sprintf("    %s", spec.Path.Value))
		}
		return "import (\n" + strings.Join(lines, ",\n") + "\n)", nil
	}
	return "", nil
}

func (re ComponentDataRenderer) getContext(ctx Ctx, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "context" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "", nil
}

func (re ComponentDataRenderer) getConfiguration(ctx Ctx, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "$config" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "{}", nil
}

func (re ComponentDataRenderer) getParameterSpec(ctx Ctx, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "parameter" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "", errors.New("no `parameter` template specified")
}

func (re ComponentDataRenderer) getParameters(ctx Ctx, file *ast.File, params interface{}) (string, error) {
	name := ctx.ProcessCtx.GetData(model.ContextName)
	if params, ok := params.(map[string]interface{}); ok {
		if params != nil && len(params) > 0 {
			bt, err := json.Marshal(params)
			if err != nil {
				return "", errors.WithMessagef(err, "marshal parameter of workload %s", name)
			}
			return string(bt), err
		}
	}
	return "", nil
}

func (re ComponentDataRenderer) getData(ctx Ctx, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "$data" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "", nil
}

func getConfigFromCueVal(ctx Ctx, key string, config cue.Value) (map[string]interface{}, error) {
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
	return common.ReadConfig(ctx.ProcessCtx.GetCtx(), singleton.KubeClient.Get(), cfgNamespaceStr, cfgNameStr)
}

func getDataFromCue(ctx process.Context, key string, data cue.Value) (cue.Value, error) {
	provider := data.LookupPath(value.FieldPath("provider"))
	if !provider.Exists() {
		return cue.Value{}, errors.New(fmt.Sprintf("provider not set in `data.%s`", key))
	}
	providerStr, _ := provider.String()

	fnVal := data.LookupPath(value.FieldPath("function"))
	if !fnVal.Exists() {
		return cue.Value{}, errors.New(fmt.Sprintf("function not set in `data.%s`", key))
	}
	fnStr, _ := fnVal.String()

	paramsAlias := data.LookupPath(value.FieldPath("params"))
	if paramsAlias.Exists() {
		data = data.FillPath(value.FieldPath("$params"), paramsAlias)
	}

	if p, ok := cuex.DefaultCompiler.Get().GetProviders()[providerStr]; ok {
		fn := p.GetProviderFn(fnStr)
		result, err := fn.Call(ctx.GetCtx(), data)
		if err != nil {
			return cue.Value{}, err
		}
		returnsVal := result.LookupPath(value.FieldPath("$returns"))
		if returnsVal.Exists() {
			return returnsVal, nil
		}
		return cue.Value{}, nil
	}
	return cue.Value{}, errors.New(fmt.Sprintf("no package %s found in compiler", providerStr))
}
