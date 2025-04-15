package render

import (
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"github.com/kubevela/pkg/cue/cuex"
	"strings"
)

type ComponentOutputRenderer struct{}

type outputRenderer interface {
	Render(ctx Ctx, rd *RenderedData, file *ast.File) (*RenderedData, error)
	getCustomFields(ctx Ctx, rd *RenderedData, file *ast.File) (string, error)
	getOutput(ctx Ctx, rd *RenderedData, file *ast.File) (string, error)
	getOutputs(ctx Ctx, rd *RenderedData, file *ast.File) (string, error)
}

func (re ComponentOutputRenderer) Render(ctx Ctx, rd *RenderedData, file *ast.File) (*RenderedData, error) {
	tmpl := strings.Builder{}
	tmpl.WriteString(rd.Template)

	fields, _ := re.getCustomFields(ctx, rd, file)
	tmpl.WriteString(fields)

	tmpl.WriteString("\n\n")

	output, _ := re.getOutput(ctx, rd, file)
	tmpl.WriteString("output: " + output)

	tmpl.WriteString("\n\n")

	outputs, _ := re.getOutputs(ctx, rd, file)
	tmpl.WriteString("outputs: " + outputs)

	cueVal, _ := cuex.DefaultCompiler.Get().CompileString(ctx.ProcessCtx.GetCtx(), tmpl.String())
	return &RenderedData{
		Template: reconstituteTemplate(rd.Imports, cueVal),
		Cue:      cueVal,
	}, nil
}

func (re ComponentOutputRenderer) getCustomFields(ctx Ctx, rd *RenderedData, file *ast.File) (string, error) {
	return getCustomFields(ctx, rd, file)
}

func (re ComponentOutputRenderer) getOutput(ctx Ctx, rd *RenderedData, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "output" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "{}", nil
}

func (re ComponentOutputRenderer) getOutputs(ctx Ctx, rd *RenderedData, file *ast.File) (string, error) {
	for _, d := range file.Decls {
		if d, ok := d.(*ast.Field); ok {
			label, _ := format.Node(d.Label)
			if string(label) == "outputs" {
				val, _ := format.Node(d.Value)
				return string(val), nil
			}
		}
	}
	return "{}", nil
}
