package render

import (
	"context"
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/tools/fix"
	"fmt"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/process"
	"slices"
	"strings"
)

var ReservedFields = []string{
	model.OutputFieldName,
	model.OutputsFieldName,
	model.ParameterFieldName,
	model.ContextFieldName,
	"$config",
}

type Ctx struct {
	CueCtx     *cue.Context
	ProcessCtx process.Context
	context.Context
}

type Renderer struct {
	ctx           Ctx
	dataRenderer  dataRenderer
	logicRenderer outputRenderer
}

func ComponentRenderer(ctx process.Context) *Renderer {
	return NewRenderer[ComponentDataRenderer, ComponentOutputRenderer](ctx)
}

func NewRenderer[D dataRenderer, L outputRenderer](ctx process.Context) *Renderer {
	rCtx := Ctx{
		ProcessCtx: ctx,
		CueCtx:     cuecontext.New(),
	}
	return &Renderer{
		ctx:           rCtx,
		dataRenderer:  dataRenderer(*new(D)),
		logicRenderer: outputRenderer(*new(L)),
	}
}

func (r *Renderer) Render(abstractTmpl string, params interface{}) (cue.Value, error) {
	return Render(r, abstractTmpl, params)
}

type RenderedData struct {
	Imports  string
	Template string
	Cue      cue.Value
}

func Render(re *Renderer, abstractTmpl string, params interface{}) (cue.Value, error) {
	baseCtx, _ := re.ctx.ProcessCtx.BaseContextFile()
	f, _ := parser.ParseFile("-", strings.Join([]string{
		abstractTmpl,
		baseCtx,
	}, "\n\n"))
	file := fix.File(f)

	rendered, _ := re.dataRenderer.Render(re.ctx, file, params)
	rendered, _ = re.logicRenderer.Render(re.ctx, rendered, file)

	syntax := rendered.Cue.Syntax(cue.Final())
	n, _ := format.Node(syntax)
	println(string(n))

	return rendered.Cue, nil
}

type ComponentRenderEngine struct{}

type TraitRenderEngine struct{}

func reconstituteTemplate(imports string, cv cue.Value) string {
	reconstitutedTmpl := strings.Builder{}
	reconstitutedTmpl.WriteString(imports)
	reconstitutedTmpl.WriteString("\n\n")
	f, _ := cv.Fields()
	for f.Next() {
		label := f.Label()
		syntax := f.Value().Syntax(cue.Final())

		attrs := make([]string, 0)
		for _, a := range f.Value().Attributes(cue.FieldAttr) {
			attrs = append(attrs, fmt.Sprintf("%s(%s)", a.Name(), a.Contents()))
		}

		n, _ := format.Node(syntax)
		reconstitutedTmpl.WriteString(label + ": " + string(n) + " " + strings.Join(attrs, " "))
		reconstitutedTmpl.WriteString("\n\n")
	}
	return reconstitutedTmpl.String()
}

func getCustomFields(ctx Ctx, rd *RenderedData, file *ast.File) (string, error) {
	existingFields := []string{
		"output", "outputs",
	}
	iter, _ := rd.Cue.Fields()
	for iter.Next() {
		existingFields = append(existingFields, iter.Label())
	}

	customFields := make([]string, 0)
	for _, decl := range file.Decls {
		if decl, ok := decl.(*ast.Field); ok {
			label, _ := format.Node(decl.Label)
			if slices.Contains(existingFields, string(label)) {
				continue
			}

			attrs := make([]string, 0)
			for _, a := range decl.Attrs {
				attrs = append(attrs, a.Text)
			}
			val, _ := format.Node(decl.Value)
			customFields = append(customFields, string(label)+": "+string(val)+" "+strings.Join(attrs, " "))
		}
	}

	return strings.Join(customFields, "\n\n"), nil
}
