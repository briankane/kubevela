package definition

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/process"
	"k8s.io/klog/v2"

	"github.com/oam-dev/kubevela/apis/types"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/template"
)

func processComposition(ctx process.Context, name string, val cue.Value, compositions cue.Value) (cue.Value, error) {
	klog.Infof("Processing Compositions")
	rootType, _ := val.LookupPath(value.FieldPath("parameter", "$type")).String()
	ctx.PushData("compositionRoot", rootType)
	ctx.PushData("compositionPath", rootType)

	components := compositions.LookupPath(value.FieldPath("components"))
	componentsIter, _ := components.Fields()

	for {
		if !componentsIter.Next() {
			break
		}
		componentKey := componentsIter.Label()
		component := componentsIter.Value()
		compositionPath := strings.Join([]string{componentKey, name}, ".")

		ctx.PushData("compositionId", componentKey)
		ctx.PushData("compositionPath", compositionPath)

		val, _ = processComponent(ctx, val, componentKey, component)
	}

	//refresh data
	compositions = val.LookupPath(value.FieldPath("composition"))
	components = compositions.LookupPath(value.FieldPath("components"))

	compositionOutputRef := compositions.LookupPath(value.FieldPath("output"))
	if compositionOutputRef.Exists() {
		if val.LookupPath(value.FieldPath("output")).Exists() {
			return cue.Value{}, errors.New("cannot declare output in base template and composition")
		}
		compositionOutputPath, _ := compositionOutputRef.String()

		compOutput := components.LookupPath(value.FieldPath(strings.Split(compositionOutputPath, ",")...))
		val = val.FillPath(value.FieldPath("output"), compOutput)
	}

	outputs := make(map[string]cue.Value)
	standardOutput := val.LookupPath(value.FieldPath("output"))
	standardOutputs := val.LookupPath(value.FieldPath("outputs"))
	if standardOutputs.Exists() {
		stdOutputsIter, _ := standardOutputs.Fields()
		for {
			if !stdOutputsIter.Next() {
				break
			}
			compKey := stdOutputsIter.Label()
			compVal := stdOutputsIter.Value()
			outputs[compKey] = compVal
		}
	}

	componentsIter, _ = components.Fields()
	for {
		if !componentsIter.Next() {
			break
		}
		compKey := componentsIter.Label()
		compVal := componentsIter.Value()
		compValOutput := compVal.LookupPath(value.FieldPath("output"))
		if !compValOutput.Equals(standardOutput) {
			outputs[compKey+".main"] = compVal.LookupPath(value.FieldPath("output"))
		}
		compOutputs := compVal.LookupPath(value.FieldPath("outputs"))
		if compOutputs.Exists() {
			compOutputsIter, _ := compOutputs.Fields()
			for {
				if !compOutputsIter.Next() {
					break
				}
				compOutputKey := compOutputsIter.Label()
				compOutputVal := compOutputsIter.Value()
				if !compOutputVal.Equals(standardOutput) {
					outputs[compOutputsIter.Label()+"."+compOutputKey] = compOutputVal
				}
			}
		}
	}
	val = val.FillPath(value.FieldPath("outputs"), outputs)
	klog.Infof("Result: %s", val)
	return val, nil
}

func processComponent(ctx process.Context, val cue.Value, componentKey string, component cue.Value) (cue.Value, error) {
	klog.Infof("Processing Component: %s", componentKey)
	c, _ := ctx.BaseContextFile()
	var compParamFile = velaprocess.ParameterFieldName + ": {}"
	typ, _ := component.LookupPath(value.FieldPath("type")).String()
	compParams := component.LookupPath(value.FieldPath("properties"))
	klog.Infof("Component Params Value: %s", compParams)
	tmpl, _ := template.LoadTemplate(ctx.GetCtx(), getClient(), typ, types.TypeComponentDefinition, make(map[string]string))

	if compParams.Exists() {
		bt, _ := json.Marshal(compParams)
		klog.Infof("Component Params: %s", bt)
		if string(bt) != "null" {
			compParamFile = fmt.Sprintf("%s: %s", velaprocess.ParameterFieldName, string(bt))
		}
	}

	compVal, _ := cuex.CompileStringWithOptions(ctx.GetCtx(), strings.Join([]string{
		renderTemplate(tmpl.TemplateStr), compParamFile, c,
	}, "\n"))
	val = val.FillPath(value.FieldPath("composition", "components", componentKey, "output"), compVal.LookupPath(value.FieldPath("output")))
	outputs := compVal.LookupPath(value.FieldPath("outputs"))
	if outputs.Exists() {
		val = val.FillPath(value.FieldPath("composition", "components", componentKey, "outputs"), outputs)
	}
	return val, nil
}
