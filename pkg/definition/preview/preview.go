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

// Package preview renders a definition file with sample values, through the
// same engine the controller renders components and traits with, so an editor
// can show what a definition produces while it is written.
package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/process"
	"sigs.k8s.io/yaml"

	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// Request is a definition file and the values to render it with.
type Request struct {
	Path   string
	Source []byte
	// Values is a values file, YAML: parameter, context and, for a trait, the
	// workload it patches. See Skeleton.
	Values []byte
}

// Object is one rendered object: "output", or "outputs.<name>".
type Object struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}

// Result is what a definition renders to.
type Result struct {
	Type string `json:"type"`
	// Objects are a component's or policy's output and outputs, or a trait's outputs.
	Objects []Object `json:"objects"`
	// Workload is, for a trait, the workload after its patch.
	Workload *Object `json:"workload,omitempty"`
	// Error is why the definition did not render with these values.
	Error string `json:"error,omitempty"`
	// Notice is why a definition of this type has nothing to preview.
	Notice string `json:"notice,omitempty"`
}

// values is a values file.
type values struct {
	Parameter map[string]interface{} `json:"parameter"`
	Context   sampleContext          `json:"context"`
	Workload  map[string]interface{} `json:"workload"`
}

// sampleContext is the context a preview renders in.
type sampleContext struct {
	Name      string `json:"name"`
	AppName   string `json:"appName"`
	Namespace string `json:"namespace"`
	Cluster   string `json:"cluster"`
}

const (
	defaultAppName   = "my-app"
	defaultNamespace = "default"
	defaultCluster   = "local"
	// previewWorkload names the component that holds a trait's sample workload.
	previewWorkload = "preview-workload"
)

var outputLine = regexp.MustCompile(`(?m)^output\s*:`)

// Render renders the definition in req with req.Values.
func Render(ctx context.Context, req Request) Result {
	tmpl, ok := analysis.TemplateSource(req.Path, req.Source)
	if !ok {
		return Result{Error: fmt.Sprintf("%s is not a definition, or does not parse", req.Path)}
	}
	res := Result{Type: tmpl.Type, Objects: []Object{}}
	var v values
	if err := yaml.Unmarshal(req.Values, &v); err != nil {
		res.Error = "values: " + err.Error()
		return res
	}
	if v.Parameter == nil {
		v.Parameter = map[string]interface{}{}
	}
	pctx := v.Context.processContext(ctx, tmpl.Name)

	switch tmpl.Type {
	case "component":
		res.fail(definition.NewWorkloadAbstractEngine(tmpl.Name).Complete(pctx, tmpl.Body, v.Parameter))
		if res.Error == "" {
			res.collect(pctx, true)
		}
	case "policy":
		if !outputLine.MatchString(tmpl.Body) {
			res.Notice = "This policy's template has no output, so it renders nothing to preview."
			return res
		}
		res.fail(definition.NewPolicyAbstractEngine(tmpl.Name).Complete(pctx, tmpl.Body, v.Parameter))
		if res.Error == "" {
			res.collect(pctx, true)
		}
	case "trait":
		workload := v.Workload
		if workload == nil {
			workload = sampleWorkload(v.Context.name(tmpl.Name))
		}
		body, err := json.Marshal(workload)
		if err != nil {
			res.Error = "workload: " + err.Error()
			return res
		}
		res.fail(definition.NewWorkloadAbstractEngine(previewWorkload).Complete(pctx, "output: "+string(body), nil))
		if res.Error == "" {
			res.fail(definition.NewTraitAbstractEngine(tmpl.Name).Complete(pctx, tmpl.Body, v.Parameter))
		}
		if res.Error == "" {
			res.collect(pctx, false)
		}
	case "workflow-step":
		res.Notice = "A workflow step runs actions rather than rendering objects, so it has no output to preview."
	case "source":
		res.Notice = "A source resolves data when an Application reads it, so it has no output to preview."
	default:
		res.Notice = fmt.Sprintf("A %s definition renders no objects to preview.", tmpl.Type)
	}
	return res
}

func (r *Result) fail(err error) {
	if err != nil && r.Error == "" {
		r.Error = err.Error()
	}
}

// collect reads what was rendered into pctx: the base as output, or for a
// trait as the patched workload, and every auxiliary as an outputs entry.
func (r *Result) collect(pctx process.Context, baseIsOutput bool) {
	base, aux := pctx.Output()
	if base != nil {
		o, err := toObject("output", base)
		if err != nil {
			r.fail(err)
			return
		}
		if baseIsOutput {
			r.Objects = append(r.Objects, o)
		} else {
			r.Workload = &o
		}
	}
	for _, a := range aux {
		o, err := toObject("outputs."+a.Name, a.Ins)
		if err != nil {
			r.fail(err)
			return
		}
		r.Objects = append(r.Objects, o)
	}
}

func toObject(name string, ins model.Instance) (Object, error) {
	u, err := ins.Unstructured()
	if err != nil {
		return Object{}, fmt.Errorf("%s: %w", name, err)
	}
	b, err := yaml.Marshal(u.Object)
	if err != nil {
		return Object{}, fmt.Errorf("%s: %w", name, err)
	}
	return Object{Name: name, YAML: string(b)}, nil
}

func (c sampleContext) name(def string) string {
	if c.Name != "" {
		return c.Name
	}
	return def
}

func (c sampleContext) processContext(ctx context.Context, def string) process.Context {
	data := velaprocess.ContextData{
		Ctx:       ctx,
		CompName:  c.name(def),
		AppName:   c.AppName,
		Namespace: c.Namespace,
		Cluster:   c.Cluster,
	}
	if data.AppName == "" {
		data.AppName = defaultAppName
	}
	if data.Namespace == "" {
		data.Namespace = defaultNamespace
	}
	if data.Cluster == "" {
		data.Cluster = defaultCluster
	}
	return velaprocess.NewContext(data)
}

// sampleWorkload is the Deployment a trait patches when the values name none.
func sampleWorkload(name string) map[string]interface{} {
	labels := map[string]interface{}{"app.oam.dev/component": name}
	return map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name},
		// No replicas, as webservice sets none: a trait's plain patch could not change it.
		"spec": map[string]interface{}{
			"selector": map[string]interface{}{"matchLabels": labels},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": labels},
				"spec": map[string]interface{}{"containers": []interface{}{
					map[string]interface{}{"name": name, "image": "nginx"},
				}},
			},
		},
	}
}

// Skeleton is a values file for the definition in src: every required
// parameter named, with its usage, and every default filled in.
func Skeleton(ctx context.Context, path string, src []byte) (string, error) {
	tmpl, ok := analysis.TemplateSource(path, src)
	if !ok {
		return "", fmt.Errorf("%s is not a definition, or does not parse", path)
	}
	pctx := sampleContext{}.processContext(ctx, tmpl.Name)
	c, err := pctx.BaseContextFile()
	if err != nil {
		return "", err
	}
	val, err := velacuex.WorkloadCompiler.Get().CompileString(ctx, tmpl.Body+"\n"+c)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Values to preview %s with. Nothing here is applied.\nparameter:", tmpl.Name)
	if p := val.LookupPath(cue.ParsePath("parameter")); p.Exists() && writeFields(&b, p, "  ") {
		b.WriteString("\n")
	} else {
		b.WriteString(" {}\n")
	}
	fmt.Fprintf(&b, "context:\n  name: %s\n  appName: %s\n  namespace: %s\n  cluster: %s\n", tmpl.Name, defaultAppName, defaultNamespace, defaultCluster)
	if tmpl.Type == "trait" {
		w, err := yaml.Marshal(sampleWorkload(tmpl.Name))
		if err != nil {
			return "", err
		}
		b.WriteString("# The workload the trait patches.\nworkload:\n")
		for _, line := range strings.Split(strings.TrimRight(string(w), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String(), nil
}

// writeFields writes the regular fields of v as YAML lines at indent, and
// reports whether it wrote any.
func writeFields(b *strings.Builder, v cue.Value, indent string) bool {
	iter, err := v.Fields()
	if err != nil {
		return false
	}
	wrote := false
	for iter.Next() {
		wrote = true
		name := iter.Selector().Unquoted()
		f := iter.Value()
		if d, ok := f.Default(); ok && d.IsConcrete() {
			fmt.Fprintf(b, "\n%s%s: %s", indent, name, jsonOf(d))
			continue
		}
		if f.IsConcrete() && f.IncompleteKind() != cue.StructKind {
			fmt.Fprintf(b, "\n%s%s: %s", indent, name, jsonOf(f))
			continue
		}
		if f.IncompleteKind() == cue.StructKind {
			fmt.Fprintf(b, "\n%s%s:", indent, name)
			if !writeFields(b, f, indent+"  ") {
				b.WriteString(" {}")
			}
			continue
		}
		fmt.Fprintf(b, "\n%s%s: %s # required%s", indent, name, placeholder(f.IncompleteKind()), usageOf(f))
	}
	return wrote
}

func jsonOf(v cue.Value) string {
	b, err := v.MarshalJSON()
	if err != nil {
		return `""`
	}
	return string(b)
}

func placeholder(k cue.Kind) string {
	switch {
	case k&cue.StringKind != 0:
		return `""`
	case k&cue.NumberKind != 0:
		return "0"
	case k&cue.BoolKind != 0:
		return "false"
	case k&cue.ListKind != 0:
		return "[]"
	default:
		return "null"
	}
}

var usagePrefix = regexp.MustCompile(`\+usage=(.*)`)

// usageOf is a field's +usage text, as ": text", or nothing.
func usageOf(v cue.Value) string {
	for _, d := range v.Doc() {
		if m := usagePrefix.FindStringSubmatch(d.Text()); m != nil {
			return ": " + strings.TrimSpace(m[1])
		}
	}
	return ""
}
