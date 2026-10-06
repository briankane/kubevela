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

package analysis

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
	cueyaml "cuelang.org/go/encoding/yaml"
	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"

	"github.com/oam-dev/kubevela/pkg/cue/cuex/providers/config"
	"github.com/oam-dev/kubevela/pkg/definition/kubeschema"
)

const cueExt = ".cue"

func isYAMLExt(ext string) bool { return ext == ".yaml" || ext == ".yml" }

// The names pkg/addon reads an addon's files by.
const (
	addonMetadataFile    = "metadata.yaml"
	addonTemplateFile    = "template.cue"
	addonParameterFile   = "parameter.cue"
	addonResourcesDir    = "resources"
	addonConfigTemplates = "config-templates"
	addonUISchemas       = "schemas"
	addonViews           = "views"
)

//go:embed addon_application.cue
var addonApplicationCUE string

// addonContextCUE is what pkg/addon gives an addon's CUE: the addon's
// metadata.yaml, as its Meta type decodes it.
const addonContextCUE = `
context: #velaAddonContext
#velaAddonContext: metadata: #velaAddonMeta
`

// addonOutputCUE is what template.cue's output must be: the Application the
// addon installs.
const addonOutputCUE = `
output?: #addonApplication & {
	apiVersion?: "core.oam.dev/v1beta1"
	kind?:       "Application"
}
`

// addonMetaCUE is pkg/addon's Meta type.
const addonMetaCUE = `
#velaAddonMeta: {
	name:        string
	version:     string
	description: string
	icon:        string
	url?:        string
	tags?: [...string]
	uxPlugins?: [string]: string
	deployTo?: {
		runtime_cluster?:     bool
		disableControlPlane?: bool
		runtimeCluster?:      bool
	}
	dependencies?: [...{name?: string, version?: string}]
	needNamespace?: [...string]
	invisible: bool
	system?: {vela?: string, kubernetes?: string}
	annotations?: [string]: string
}
`

// configTemplateCUE is what pkg/config compiles a config template with: its
// context, and the shape its parser reads.
const configTemplateCUE = `
context: #velaConfigContext
metadata: #velaConfigMetadata
template: #velaConfigTemplate
#velaConfigContext: {
	name:      string
	namespace: string
	config?: [...{name: string, value: string}]
}
#velaConfigMetadata: {
	name:         string
	alias?:       string
	description?: string
	sensitive?:   bool
	scope?:       "system" | "namespace"
}
#velaConfigTemplate: {
	output?: #velaConfigSecret
	outputs?: [string]: {...}
	validation?: {...}
	nacos?: {...}
	...
}
`

// uiSchemaCUE is pkg/utils/schema's UISchema: a list of UIParameter.
const uiSchemaCUE = `
#UISchema: [...#UIParameter]
#UIParameter: {
	sort?:        int & >=0
	label?:       string
	description?: string
	validate?:    #Validate
	jsonKey?:     string
	uiType?:      string
	style?: colSpan?: int
	disable?: bool
	conditions?: [...#Condition]
	subParameterGroupOption?: [...{label?: string, keys?: [...string]}]
	subParameters?: [...#UIParameter]
	additionalParameter?: #UIParameter
	additional?:          bool
}
#Condition: {
	jsonKey: string
	op?:     "==" | "!=" | "in"
	value?:  _
	action?: "enable" | "disable"
}
#Validate: {
	required?:     bool
	max?:          number
	maxLength?:    int & >=0
	min?:          number
	minLength?:    int & >=0
	pattern?:      string
	options?: [...{label?: string, value?: _}]
	defaultValue?: _
	immutable?:    bool
}
`

// uiSchemaName is the name VelaUX finds a definition's UI schema by: an
// addon installs each file as a ConfigMap named after it.
var uiSchemaName = regexp.MustCompile(`^(component|trait|policy|workflowstep)-uischema-[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

var configPackages = &packageSet{list: func() []cuexruntime.Package { return []cuexruntime.Package{config.Package} }}

// AddonRoot is the addon a file belongs to: the nearest folder above it, at
// most three up, holding a metadata.yaml.
func AddonRoot(path string) (string, bool) {
	dir := filepath.Dir(path)
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(filepath.Join(dir, addonMetadataFile)); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// CheckAddonFile checks an addon's file the way KubeVela reads it: its
// template.cue, a config template, a UI schema or a view. It is false for
// any other file, which is checked as what it is: definitions as
// definitions.
func CheckAddonFile(path string, src []byte, opts Options) ([]Diagnostic, bool) {
	root, ok := AddonRoot(path)
	if !ok {
		return nil, false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	ext := filepath.Ext(path)
	d := &document{path: path, src: src, opts: opts}
	var diags []Diagnostic
	switch {
	case rel == addonMetadataFile:
		diags = d.checkAddonMetadata()
	case rel == "metadata.cue":
		diags = []Diagnostic{d.warnFirstLine("KubeVela reads an addon's metadata from metadata.yaml: this file is not read")}
	case rel == addonTemplateFile:
		diags = d.checkAddonTemplate(root)
	case len(parts) == 2 && parts[0] == addonResourcesDir && (isYAMLExt(ext)):
		diags = d.checkObjectsYAML()
	case len(parts) == 2 && parts[0] == addonResourcesDir && ext == cueExt && parts[1] != addonParameterFile:
		diags = d.checkAddonResource(root)
	case len(parts) == 2 && parts[0] == addonConfigTemplates && ext == cueExt:
		diags = d.checkConfigTemplate()
	case len(parts) == 2 && (parts[0] == addonUISchemas || parts[0] == "uischemas") && (isYAMLExt(ext)):
		diags = d.checkUISchema(parts[0])
	case len(parts) == 2 && parts[0] == addonViews && ext == cueExt:
		diags = d.checkView()
	default:
		return nil, false
	}
	return sortDiagnostics(firstPerPosition(diags)), true
}

// parse parses the document, or reports why it cannot be.
func (d *document) parse() ([]Diagnostic, bool) {
	f, err := parser.ParseFile(d.path, d.src, parser.ParseComments)
	if err != nil {
		return d.fromErrors(err, ""), false
	}
	d.file = f
	return nil, true
}

// checkImports reports an import of a vela/ package the reader does not
// offer, which allowed names.
func (d *document) checkImports(reader string, allowed map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for _, spec := range d.file.Imports {
		p := strings.Trim(spec.Path.Value, `"`)
		if strings.HasPrefix(p, "vela/") && !allowed[p] {
			diags = append(diags, d.at(spec.Path.Pos(), fmt.Sprintf("%s: %s is not available here", reader, p)))
		}
	}
	return diags
}

// build compiles the document with extra, a source in the same package, and
// any files given, over imports.
func (d *document) build(ctx *cue.Context, imports []*build.Instance, extra string, files ...*ast.File) (cue.Value, []Diagnostic) {
	if d.file.PackageName() == "" {
		d.file.Decls = append([]ast.Decl{&ast.Package{Name: ast.NewIdent("main")}}, d.file.Decls...)
	}
	pkg := d.file.PackageName()
	bi := build.NewContext().NewInstance(filepath.Dir(d.path), nil)
	bi.Imports = imports
	header := ""
	if pkg != "" {
		header = "package " + pkg + "\n"
	}
	ef, err := parser.ParseFile("vela-context.cue", header+extra)
	if err != nil {
		return cue.Value{}, []Diagnostic{d.at(token.NoPos, err.Error())}
	}
	// The context goes first: CUE reports a read of a closed struct declared
	// after it as incomplete, so not at all.
	for _, f := range append([]*ast.File{ef, d.file}, files...) {
		if err := bi.AddSyntax(f); err != nil {
			return cue.Value{}, d.fromErrors(err, "")
		}
	}
	v := ctx.BuildInstance(bi)
	if v.Err() != nil {
		return v, d.placed(v.Err())
	}
	return v, append(d.placed(v.Validate()), d.checkReads(v)...)
}

// checkReads checks each parameter and context chain the document reads
// against their closed declarations, which catches a read CUE does not
// evaluate, such as one under an undecided if.
func (d *document) checkReads(v cue.Value) []Diagnostic {
	param := v.LookupPath(cue.MakePath(cue.Def("#velaAddonParameter")))
	ctx := v.LookupPath(cue.ParsePath("context"))
	var diags []Diagnostic
	ast.Walk(d.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root, chain := flatten(sel)
		switch {
		case root == nil:
			return true
		case root.Name == parameterLabel && param.Exists():
			if diag, bad := d.checkParameter(param, chain); bad {
				diags = append(diags, diag)
			}
		case root.Name == "context" && ctx.Exists():
			if diag, bad := d.checkContext(ctx, chain); bad {
				diags = append(diags, diag)
			}
		}
		return false
	}, nil)
	return diags
}

// placed are an error's diagnostics in this document, or, where it names no
// position here, at the field its path names.
func (d *document) placed(err error) []Diagnostic {
	var diags []Diagnostic
	for _, e := range cueerrors.Errors(err) {
		got := d.fromErrors(e, "")
		if len(got) == 0 {
			format, args := e.Msg()
			msg := fmt.Sprintf(format, args...)
			if p := strings.Join(e.Path(), "."); p != "" {
				msg = p + ": " + msg
			}
			got = []Diagnostic{d.at(d.fieldPos(e.Path()), msg)}
		}
		diags = append(diags, got...)
	}
	return diags
}

// fieldPos is the label of the deepest field of path the document declares,
// or its start.
func (d *document) fieldPos(path []string) token.Pos {
	pos := token.NoPos
	var decls []ast.Decl
	if d.file != nil {
		decls = d.file.Decls
	}
	for _, seg := range path {
		var next []ast.Decl
		for _, decl := range decls {
			f, ok := decl.(*ast.Field)
			if !ok {
				continue
			}
			if labelName(f.Label) == strings.Trim(seg, `"`) {
				pos = f.Label.Pos()
				if s, ok := f.Value.(*ast.StructLit); ok {
					next = append(next, s.Elts...)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		decls = next
	}
	if !pos.IsValid() {
		return token.NoPos
	}
	return pos
}

// requireFields reports each field path names, dotted, that the document
// does not declare, on its first line.
func (d *document) requireFields(why string, paths ...string) []Diagnostic {
	var diags []Diagnostic
	for _, p := range paths {
		if !d.declares(strings.Split(p, ".")) {
			diags = append(diags, d.firstLine(fmt.Sprintf("%s must be set: %s", p, why)))
		}
	}
	return diags
}

// declares reports whether the document declares the field at path.
func (d *document) declares(path []string) bool {
	decls := d.file.Decls
	for i, seg := range path {
		var next []ast.Decl
		found := false
		for _, decl := range decls {
			f, ok := decl.(*ast.Field)
			if !ok || labelName(f.Label) != seg {
				continue
			}
			found = true
			if s, ok := f.Value.(*ast.StructLit); ok {
				next = append(next, s.Elts...)
			}
		}
		if !found {
			return false
		}
		if i < len(path)-1 && len(next) == 0 {
			return false
		}
		decls = next
	}
	return true
}

// firstLine is a diagnostic on the document's first line.
func (d *document) firstLine(msg string) Diagnostic {
	end := strings.IndexByte(string(d.src), '\n')
	if end < 0 {
		end = len(d.src)
	}
	return Diagnostic{Range: Range{Start: Position{Line: 1, Column: 1}, End: Position{Line: 1, Column: end + 1}}, Severity: SeverityError, Message: msg}
}

// checkAddonTemplate checks template.cue as pkg/addon renders it: with the
// addon's parameter.cue and the resources/*.cue of its package, its context,
// and CUE's standard library only. Its output is the Application the addon
// installs, and its outputs are objects installed beside it.
func (d *document) checkAddonTemplate(root string) []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	if diags := d.checkImports("an addon's template.cue compiles with CUE's standard library only", nil); len(diags) > 0 {
		return diags
	}
	if d.file.PackageName() == "" {
		d.file.Decls = append([]ast.Decl{&ast.Package{Name: ast.NewIdent("main")}}, d.file.Decls...)
	}
	param, resources, extra := addonPackage(root, d.file.PackageName(), d.path)
	files := withParam(param, resources...)
	extra += addonOutputCUE
	v, diags := d.build(cuecontext.New(), nil, extra, files...)
	if !v.Exists() {
		return diags
	}
	diags = append(diags, d.requireFields("it is the Application the addon installs", "output")...)
	diags = append(diags, d.checkTyped(v.LookupPath(cue.ParsePath("output.spec")))...)
	diags = append(diags, d.checkAuxiliary(v.LookupPath(cue.ParsePath("outputs")), "outputs")...)
	return diags
}

// typedLists are the lists of an Application's spec whose items name their
// type, and what else each item must name.
var typedLists = []struct {
	path     string
	required []string
}{
	{"components", []string{"name", "type"}},
	{"policies", []string{"type"}},
	{"workflow.steps", []string{"type"}},
}

// checkTyped reports the items of an Application's spec missing a field the
// CRD requires: CUE only enforces a required field on a concrete value.
func (d *document) checkTyped(spec cue.Value) []Diagnostic {
	var diags []Diagnostic
	check := func(list cue.Value, where string, required []string) {
		it, err := list.List()
		if err != nil {
			return
		}
		for i := 0; it.Next(); i++ {
			for _, field := range required {
				if it.Value().LookupPath(cue.ParsePath(field)).Exists() {
					continue
				}
				pos := it.Value().Pos()
				if !pos.IsValid() || pos.Filename() != d.path {
					pos = d.fieldPos([]string{"output"})
				}
				diags = append(diags, d.at(pos, fmt.Sprintf("output.spec.%s[%d] has no %s", where, i, field)))
			}
			if where == "components" {
				traits, _ := it.Value().LookupPath(cue.ParsePath("traits")).List()
				for j := 0; traits.Next(); j++ {
					if !traits.Value().LookupPath(cue.ParsePath("type")).Exists() {
						diags = append(diags, d.at(traits.Value().Pos(), fmt.Sprintf("output.spec.components[%d].traits[%d] has no type", i, j)))
					}
				}
			}
		}
	}
	for _, l := range typedLists {
		check(spec.LookupPath(cue.ParsePath(l.path)), l.path, l.required)
	}
	return diags
}

// addonPackage is what pkg/addon compiles beside a file of package pkg:
// parameter.cue, if there is one, and the resources/*.cue of the package,
// less the file at self. extra is the context they are given.
func addonPackage(root, pkg, self string) (param *ast.File, resources []*ast.File, extra string) {
	extra = addonContextCUE + addonMetaCUE + addonApplicationCUE
	paramPath := filepath.Join(root, addonParameterFile)
	if _, err := os.Stat(paramPath); err != nil {
		paramPath = filepath.Join(root, addonResourcesDir, addonParameterFile)
	}
	if f, ok := addonFile(paramPath, pkg, true); ok {
		param = f
		extra += "#velaAddonParameter: parameter\n"
	}
	paths, _ := filepath.Glob(filepath.Join(root, addonResourcesDir, "*.cue"))
	for _, r := range paths {
		if filepath.Base(r) == addonParameterFile || r == self {
			continue
		}
		if f, ok := addonFile(r, pkg, false); ok {
			resources = append(resources, f)
		}
	}
	return param, resources, extra
}

// withParam is files with param first, when there is one.
func withParam(param *ast.File, files ...*ast.File) []*ast.File {
	if param == nil {
		return files
	}
	return append([]*ast.File{param}, files...)
}

// addonFile is a CUE file pkg/addon compiles with template.cue: of its
// package, or, when unnamed is set, of none.
func addonFile(path, pkg string, unnamed bool) (*ast.File, bool) {
	//nolint:gosec // reading the addon's own files is the point
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	f, err := parser.ParseFile(path, src)
	if err != nil {
		return nil, false
	}
	switch {
	case f.PackageName() == pkg:
	case f.PackageName() == "" && unnamed:
		f.Decls = append([]ast.Decl{&ast.Package{Name: ast.NewIdent(pkg)}}, f.Decls...)
	default:
		return nil, false
	}
	return f, true
}

// checkAuxiliary reports the entries of a map of objects that are not ones:
// each needs an apiVersion and a kind.
func (d *document) checkAuxiliary(objects cue.Value, label string) []Diagnostic {
	it, err := objects.Fields()
	if err != nil {
		return nil
	}
	var diags []Diagnostic
	for it.Next() {
		for _, field := range []string{"apiVersion", "kind"} {
			if _, err := it.Value().LookupPath(cue.ParsePath(field)).String(); err != nil {
				pos := d.fieldPos([]string{label, it.Selector().Unquoted()})
				diags = append(diags, d.at(pos, fmt.Sprintf("%s.%s has no %s: each is a Kubernetes object", label, it.Selector().Unquoted(), field)))
			}
		}
	}
	return diags
}

var (
	secretOnce sync.Once
	secretCUE  string
)

// secretSchema is CUE declaring #velaConfigSecret, a v1 Secret.
func secretSchema() string {
	secretOnce.Do(func() {
		secretCUE = "#velaConfigSecret: {...}\n"
		kinds, err := kubeschema.Builtin()
		if err != nil {
			return
		}
		gvk := kubeschema.ParseGVK("v1", "Secret")
		if src, ok := kinds.CUE(gvk); ok {
			secretCUE = "#velaConfigSecret: " + kubeschema.Root(gvk) + "\n" + src
		}
	})
	return secretCUE
}

// checkConfigTemplate checks a config template as pkg/config parses it.
func (d *document) checkConfigTemplate() []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	if diags := d.checkImports("a config template may import vela/config only", map[string]bool{"vela/config": true}); len(diags) > 0 {
		return diags
	}
	extra := configTemplateCUE + secretSchema()
	if d.declares([]string{"template", parameterLabel}) {
		extra += "#velaAddonParameter: template.parameter\n"
	}
	v, diags := d.build(cuecontext.New(), configPackages.imports(), extra)
	if !v.Exists() {
		return diags
	}
	diags = append(diags, d.requireFields("a config template is read from metadata and template", "metadata", "template")...)
	if d.declares([]string{"template"}) {
		diags = append(diags, d.requireFields("it is the form a config is created from", "template.parameter")...)
	}
	return diags
}

// checkView checks a VelaQL view: it compiles with the workflow packages, and
// returns its status or export.
func (d *document) checkView() []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	v, diags := d.build(cuecontext.New(), workflowPackages.imports(), "")
	if !v.Exists() {
		return diags
	}
	if !v.LookupPath(cue.ParsePath("status")).Exists() && !v.LookupPath(cue.ParsePath("export")).Exists() {
		diags = append(diags, d.firstLine("a view must set status or export: it is what the query returns"))
	}
	return diags
}

// checkUISchema checks a UI schema as VelaUX reads it, and that it is where
// and under the name it will be found.
func (d *document) checkUISchema(dir string) []Diagnostic {
	var diags []Diagnostic
	name := strings.TrimSuffix(filepath.Base(d.path), filepath.Ext(d.path))
	switch {
	case dir != addonUISchemas:
		diags = append(diags, d.warnFirstLine("KubeVela reads an addon's UI schemas from schemas/: this folder is not installed"))
	case !uiSchemaName.MatchString(name):
		want := "<type>-uischema-<name>"
		if parts := strings.SplitN(name, "-", 2); len(parts) == 2 && uiSchemaName.MatchString(parts[0]+"-uischema-"+parts[1]) {
			want = parts[0] + "-uischema-" + parts[1]
		}
		diags = append(diags, d.warnFirstLine(fmt.Sprintf("VelaUX finds a UI schema by its name, %s: this file installs as %s, which nothing reads", want, name)))
	}
	f, err := cueyaml.Extract(d.path, d.src)
	if err != nil {
		return append(diags, d.fromErrors(err, "")...)
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return append(diags, d.fromErrors(data.Err(), "")...)
	}
	schema := ctx.CompileString(uiSchemaCUE).LookupPath(cue.ParsePath("#UISchema"))
	return append(diags, d.fromErrors(schema.Unify(data).Validate(), "#UISchema")...)
}

func (d *document) warnFirstLine(msg string) Diagnostic {
	diag := d.firstLine(msg)
	diag.Severity = SeverityWarning
	return diag
}

// addonMainPackage is the package whose resources pkg/addon merges into
// template.cue rather than rendering on their own.
const addonMainPackage = "main"

// addonMetadataCUE is metadata.yaml as pkg/addon's Meta decodes it, with
// what KubeVela needs of it.
const addonMetadataCUE = `
#metadata: #velaAddonMeta
`

// checkAddonMetadata checks metadata.yaml against the Meta type pkg/addon
// decodes it into, which drops a key it does not know.
func (d *document) checkAddonMetadata() []Diagnostic {
	f, err := cueyaml.Extract(d.path, d.src)
	if err != nil {
		return d.fromErrors(err, "")
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return d.fromErrors(data.Err(), "")
	}
	var diags []Diagnostic
	for _, field := range []string{"name", "version"} {
		if !data.LookupPath(cue.ParsePath(field)).Exists() {
			diags = append(diags, d.firstLine(field+" must be set: the addon is enabled and upgraded by its name and version"))
		}
	}
	schema := ctx.CompileString(addonMetaCUE + addonMetadataCUE).LookupPath(cue.ParsePath("#metadata"))
	return append(diags, d.fromErrors(schema.Unify(data).Validate(), "#metadata")...)
}

// checkObjectsYAML checks a stream of Kubernetes objects: each names its
// apiVersion, kind and name, and matches its kind's schema where one is
// known.
func (d *document) checkObjectsYAML() []Diagnostic {
	f, err := cueyaml.Extract(d.path, d.src)
	if err != nil {
		return d.fromErrors(err, "")
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return d.fromErrors(data.Err(), "")
	}
	objects := []cue.Value{data}
	if it, err := data.List(); err == nil {
		objects = nil
		for it.Next() {
			objects = append(objects, it.Value())
		}
	}
	var diags []Diagnostic
	for _, obj := range objects {
		diags = append(diags, d.checkObject(ctx, obj)...)
	}
	return diags
}

// checkObject checks one Kubernetes object read from YAML.
func (d *document) checkObject(ctx *cue.Context, obj cue.Value) []Diagnostic {
	var diags []Diagnostic
	var gvk [2]string
	for i, field := range []string{"apiVersion", "kind"} {
		s, err := obj.LookupPath(cue.ParsePath(field)).String()
		if err != nil {
			diags = append(diags, d.at(valuePos(obj), "a Kubernetes object needs a "+field))
		}
		gvk[i] = s
	}
	if _, err := obj.LookupPath(cue.ParsePath("metadata.name")).String(); err != nil {
		pos := valuePos(obj.LookupPath(cue.ParsePath("metadata")))
		if !pos.IsValid() {
			pos = valuePos(obj)
		}
		diags = append(diags, d.at(pos, "a Kubernetes object needs a metadata.name"))
	}
	if len(diags) > 0 || d.opts.Kinds == nil {
		return diags
	}
	kind := kubeschema.ParseGVK(gvk[0], gvk[1])
	src, ok := d.opts.Kinds.CUE(kind)
	if !ok {
		return nil
	}
	root := kubeschema.Root(kind)
	schema := ctx.CompileString(src).LookupPath(cue.ParsePath(root))
	return d.fromErrors(schema.Unify(obj).Validate(), root)
}

// checkAddonResource checks a resources/*.cue file. One of template.cue's
// package is part of it, so is checked with it; any other renders a
// component of its own from its output.
func (d *document) checkAddonResource(root string) []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	if diags := d.checkImports("an addon's CUE compiles with CUE's standard library only", nil); len(diags) > 0 {
		return diags
	}
	if d.file.PackageName() == addonMainPackage {
		tmpl := filepath.Join(root, addonTemplateFile)
		main, ok := addonFile(tmpl, addonMainPackage, true)
		if !ok {
			return []Diagnostic{d.warnFirstLine("a file of package main is read only as part of template.cue, which this addon does not have in package main: it is not read")}
		}
		param, resources, extra := addonPackage(root, addonMainPackage, d.path)
		_, diags := d.build(cuecontext.New(), nil, extra+addonOutputCUE, withParam(param, append(resources, main)...)...)
		return diags
	}
	if !d.declares([]string{"output"}) {
		return []Diagnostic{d.warnFirstLine("this file has no output: KubeVela renders a component from each resources/*.cue outside package main, and skips one without")}
	}
	pkg := d.file.PackageName()
	if pkg == "" {
		pkg = addonMainPackage
	}
	// Only parameter.cue joins a component's file: other resources do not.
	param, _, extra := addonPackage(root, pkg, d.path)
	extra += "#velaAddonComponent: (#addonApplication & {spec: components: [_]}).spec.components[0]\nvelaAddonComponent: #velaAddonComponent & output\n"
	v, diags := d.build(cuecontext.New(), nil, extra, withParam(param)...)
	if v.Exists() && !v.LookupPath(cue.ParsePath("output.type")).Exists() {
		diags = append(diags, d.at(d.fieldPos([]string{"output"}), "output has no type: it is the component the file renders"))
	}
	return diags
}

// valuePos is where a value starts: its own position, or, for a struct read
// from YAML, which has none, its first field's.
func valuePos(v cue.Value) token.Pos {
	if pos := v.Pos(); pos.IsValid() && pos.Line() > 0 {
		return pos
	}
	it, err := v.Fields()
	if err != nil || !it.Next() {
		return token.NoPos
	}
	return valuePos(it.Value())
}
