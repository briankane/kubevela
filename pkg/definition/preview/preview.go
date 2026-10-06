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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"cuelang.org/go/cue"
	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/process"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	"github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/cue/definition/health"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
	"github.com/oam-dev/kubevela/pkg/definition/cuetest"
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
	// Inputs are what each input renders to, when the values file holds
	// several, separated by ---. Objects, Workload and Error are then empty.
	Inputs []Input `json:"inputs,omitempty"`
	// Status is what KubeVela reports of what rendered, for a component or
	// trait with a status.
	Status *Status `json:"status,omitempty"`
}

// Input is what one input of a values file renders to.
type Input struct {
	// Name is the input's own name:, or its number.
	Name     string   `json:"name"`
	Objects  []Object `json:"objects"`
	Workload *Object  `json:"workload,omitempty"`
	Error    string   `json:"error,omitempty"`
	Status   *Status  `json:"status,omitempty"`
}

// values is one input of a values file.
type values struct {
	Name      string                 `json:"name"`
	Parameter map[string]interface{} `json:"parameter"`
	Context   sampleContext          `json:"context"`
	Workload  map[string]interface{} `json:"workload"`
	// Observed is the live state of what renders, merged over it, for the
	// status to read.
	Observed cuetest.Observed `json:"observed"`
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

// Render renders the definition in req with req.Values. A values file of
// several documents, separated by ---, renders each on its own, as Inputs.
func Render(ctx context.Context, req Request) Result {
	docs, err := splitDocuments(req.Values)
	if err != nil {
		return Result{Error: "values: " + err.Error(), Objects: []Object{}}
	}
	if len(docs) <= 1 {
		one := req
		if len(docs) == 1 {
			one.Values = docs[0]
		}
		return render(ctx, one)
	}
	var res Result
	for i, doc := range docs {
		one := req
		one.Values = doc
		r := render(ctx, one)
		if r.Notice != "" {
			return r
		}
		var named struct {
			Name string `json:"name"`
		}
		_ = yaml.Unmarshal(doc, &named)
		if named.Name == "" {
			named.Name = fmt.Sprintf("input %d", i+1)
		}
		res.Type = r.Type
		res.Inputs = append(res.Inputs, Input{Name: named.Name, Objects: r.Objects, Workload: r.Workload, Error: r.Error, Status: r.Status})
	}
	res.Objects = []Object{}
	return res
}

// splitDocuments is the non-empty documents of a YAML stream.
func splitDocuments(data []byte) ([][]byte, error) {
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var docs [][]byte
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(stripComments(doc))) > 0 {
			docs = append(docs, doc)
		}
	}
}

// stripComments drops YAML comment lines, so a document of only comments
// counts as empty.
func stripComments(doc []byte) []byte {
	var out [][]byte
	for _, line := range bytes.Split(doc, []byte("\n")) {
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
			out = append(out, line)
		}
	}
	return bytes.Join(out, []byte("\n"))
}

// render renders the definition in req with one input.
func render(ctx context.Context, req Request) Result {
	if root, ok := addonPreviewed(req.Path); ok {
		return renderAddon(root, req)
	}
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
	if res.Error == "" && (tmpl.Type == "component" || tmpl.Type == "trait") && declaresStatus.Match(req.Source) {
		res.Status = previewStatus(req.Source, v, tmpl.Name)
	}
	return res
}

// declaresStatus matches a definition that says how its status is read.
var declaresStatus = regexp.MustCompile(`\b(healthPolicy|customStatus|details)\s*:`)

// Status is what KubeVela reports of a component or trait: its health,
// message and details and, for a component, each attached trait's.
type Status struct {
	Healthy bool                            `json:"healthy"`
	Message string                          `json:"message,omitempty"`
	Details map[string]string               `json:"details,omitempty"`
	Traits  map[string]*health.StatusResult `json:"traits,omitempty"`
	Error   string                          `json:"error,omitempty"`
}

// previewStatus evaluates the definition's status as the controller's health
// check does, over what renders with the values' observed state merged in.
func previewStatus(src []byte, v values, name string) *Status {
	subject, err := cuetest.SubjectFromCUE(string(src))
	if err != nil {
		return &Status{Error: err.Error()}
	}
	in := cuetest.Input{
		Parameter: v.Parameter,
		Workload:  v.Workload,
		Context: cuetest.Context{
			Name:      v.Context.name(name),
			AppName:   orDefault(v.Context.AppName, defaultAppName),
			Namespace: orDefault(v.Context.Namespace, defaultNamespace),
			Cluster:   orDefault(v.Context.Cluster, defaultCluster),
		},
	}
	report, err := cuetest.Status(subject, in, v.Observed)
	out := &Status{}
	if report != nil && report.StatusResult != nil {
		out.Healthy, out.Message, out.Details = report.Healthy, report.Message, report.Details
		if len(report.Traits) > 0 {
			out.Traits = report.Traits
		}
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
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
	if root, ok := addonPreviewed(path); ok {
		return addonSkeleton(root, path, src)
	}
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
	if declaresStatus.Match(src) {
		switch tmpl.Type {
		case "component":
			b.WriteString("# The live state its status reads, merged over what renders.\nobserved:\n  output:\n    status: {}\n")
		case "trait":
			b.WriteString("# The live state of its outputs its status reads, by name, merged over what renders.\nobserved:\n  outputs: {}\n")
		}
	}
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
		// null is a placeholder no parameter accepts, so a render names what is
		// still to fill in rather than rendering an empty value.
		fmt.Fprintf(b, "\n%s%s: null # required %s%s", indent, name, kindName(f.IncompleteKind()), usageOf(f))
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

// kindName names the kind a required parameter takes.
func kindName(k cue.Kind) string {
	switch {
	case k&cue.StringKind != 0:
		return "string"
	case k&cue.IntKind != 0 && k&cue.FloatKind == 0:
		return "int"
	case k&cue.NumberKind != 0:
		return "number"
	case k&cue.BoolKind != 0:
		return "bool"
	case k&cue.ListKind != 0:
		return "list"
	default:
		return "value"
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
