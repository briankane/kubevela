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
	"cuelang.org/go/cue/ast/astutil"
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
	addonTemplateYAML    = "template.yaml"
	addonNotesFile       = "NOTES.cue"
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
	// The addon's name: what it is enabled and upgraded by.
	name: string
	// The addon's version, compared on upgrade.
	version: string
	// What the addon does, shown in VelaUX and by vela addon list.
	description: string
	// The URL of the addon's icon in VelaUX.
	icon: string
	// The URL of the project the addon installs.
	url?: string
	// Tags to find the addon by.
	tags?: [...string]
	// VelaUX plugins the addon brings, by name, with the URL each is fetched from.
	uxPlugins?: [string]: string
	// Where the addon's Application is deployed (legacy addons: a template.cue sets its own topology).
	deployTo?: {
		runtime_cluster?: bool
		// Deploy nothing to the control plane.
		disableControlPlane?: bool
		// Deploy to the runtime clusters too.
		runtimeCluster?: bool
	}
	// Addons enabled before this one.
	dependencies?: [...{
		// The addon depended on.
		name?: string
		// The versions accepted, as in >=1.2.0.
		version?: string
	}]
	// Namespaces created in every cluster the addon deploys to.
	needNamespace?: [...string]
	// Hides the addon from VelaUX and vela addon list.
	invisible: bool
	// The versions of KubeVela and Kubernetes the addon needs, as in >=1.9.0.
	system?: {
		vela?:       string
		kubernetes?: string
	}
	// Annotations for the addon's maintainers to describe or extend it.
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
	scope?:       "system" | "namespace" | "project"
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
	// Where the field comes in the form; lower first.
	sort?: int & >=0
	// The field's label in the form.
	label?: string
	// The help text under the field.
	description?: string
	// The rules a value must meet.
	validate?: #Validate
	// The parameter this describes, by its key.
	jsonKey?: string
	// The widget the field uses, such as Input, Select, Number, Switch, Strings, Numbers, Structs, Group, KV, ImageInput, SecretSelect, CPUNumber or MemoryNumber.
	uiType?: string
	// The field's layout.
	style?: {
		// How many of the form's 24 columns the field spans.
		colSpan?: int
	}
	// Disables the field in the form.
	disable?: bool
	// When the field is enabled: it is disabled unless every enable condition holds, and by any disable condition that holds.
	conditions?: [...#Condition]
	// Groups of sub-parameters shown as alternatives.
	subParameterGroupOption?: [...{label?: string, keys?: [...string]}]
	// The fields of a struct parameter.
	subParameters?: [...#UIParameter]
	// The form of each value of a map parameter.
	additionalParameter?: #UIParameter
	// Lets the user add keys of their own.
	additional?: bool
}
#Condition: {
	// The parameter the condition reads, by its key.
	jsonKey: string
	// How the parameter is compared with value.
	op?: "==" | "!=" | "in"
	// The value compared with.
	value?: _
	// What the condition does when it holds.
	action?: "enable" | "disable"
}
#Validate: {
	// A value must be given.
	required?: bool
	// The largest number accepted.
	max?: number
	// The longest string accepted.
	maxLength?: int & >=0
	// The smallest number accepted.
	min?: number
	// The shortest string accepted.
	minLength?: int & >=0
	// A regular expression the value must match.
	pattern?: string
	// The values offered, for a Select.
	options?: [...{label?: string, value?: _}]
	// The value the form starts with.
	defaultValue?: _
	// Once set, the value cannot be changed.
	immutable?: bool
}
`

// uiSchemaName is the name VelaUX finds a UI schema by: a definition's, a
// config template's or an addon's own. An addon installs each file as a
// ConfigMap named after it.
var uiSchemaName = regexp.MustCompile(`^(component|trait|policy|workflowstep|config|addon)-uischema-[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

var configPackages = &packageSet{list: func() []cuexruntime.Package { return []cuexruntime.Package{config.Package} }}

// addonDepth is how far above a file its addon's root may be: resources/
// can nest a chart's layout several folders deep.
const addonDepth = 8

// AddonRoot is the addon a file belongs to: the nearest folder above it
// holding a metadata.yaml.
func AddonRoot(path string) (string, bool) {
	dir := filepath.Dir(path)
	for i := 0; i < addonDepth; i++ {
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
	case rel == addonTemplateYAML:
		diags = d.checkTemplateYAML(root)
	case rel == addonParameterFile || rel == addonResourcesDir+"/"+addonParameterFile:
		diags = d.checkAddonParameter()
	case rel == addonNotesFile:
		diags = d.checkNotes(root)
	case len(parts) >= 2 && parts[0] == addonResourcesDir && isYAMLExt(ext):
		diags = d.checkObjectsYAML()
	case len(parts) >= 2 && parts[0] == addonResourcesDir && ext == cueExt:
		diags = d.checkAddonResource(root)
	case len(parts) == 2 && parts[0] == addonConfigTemplates && ext == cueExt:
		diags = d.checkConfigTemplate()
	case len(parts) == 2 && (parts[0] == addonUISchemas || parts[0] == "uischemas") && isYAMLExt(ext):
		diags = d.checkUISchema(parts[0])
	case len(parts) == 2 && parts[0] == addonViews && ext == cueExt:
		diags = d.checkView()
	default:
		return nil, false
	}
	return sortDiagnostics(firstPerPosition(diags)), true
}

// Addon CUE file kinds, by what they compile with.
const (
	addonKindTemplate  = "template"
	addonKindResource  = "resource"
	addonKindConfig    = "config"
	addonKindView      = "view"
	addonKindNotes     = "notes"
	addonKindParameter = "parameter"
)

// addonCUEFile is the addon and kind of an addon's CUE file at path.
func addonCUEFile(path string) (root, kind string, ok bool) {
	root, ok = AddonRoot(path)
	if !ok || filepath.Ext(path) != cueExt {
		return "", "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case rel == addonTemplateFile:
		return root, addonKindTemplate, true
	case rel == addonParameterFile || rel == addonResourcesDir+"/"+addonParameterFile:
		return root, addonKindParameter, true
	case rel == addonNotesFile:
		return root, addonKindNotes, true
	case len(parts) >= 2 && parts[0] == addonResourcesDir:
		return root, addonKindResource, true
	case len(parts) == 2 && parts[0] == addonConfigTemplates:
		return root, addonKindConfig, true
	case len(parts) == 2 && parts[0] == addonViews:
		return root, addonKindView, true
	}
	return "", "", false
}

// addonCompile is what an addon's CUE file compiles with: the packages it
// may import, the CUE KubeVela gives it, and the files compiled beside it.
// output names the field its output is checked under.
type addonCompile struct {
	pkgs   *packageSet
	extra  string
	files  []*ast.File
	output string
}

// stdlibOnly is the package set of addon CUE that pkg/addon compiles with no
// KubeVela packages.
var stdlibOnly = &packageSet{list: func() []cuexruntime.Package { return nil }}

// mainTemplateMissing is the reason a resource of package main is not read.
const mainTemplateMissing = "a file of package main is read only as part of template.cue, which this addon does not have in package main: it is not read"

// componentCUE checks a resource file's output as an Application's component.
const componentCUE = "#velaAddonComponent: (#addonApplication & {spec: components: [_]}).spec.components[0]\nvelaAddonComponent: #velaAddonComponent & output\n"

// addonCompile is how the parsed document compiles, as the kind of addon
// file it is. It is false, with why, when KubeVela does not read it.
func (d *document) addonCompile(root, kind string) (addonCompile, string, bool) {
	switch kind {
	case addonKindTemplate:
		if d.file.PackageName() == "" {
			d.file.Decls = append([]ast.Decl{&ast.Package{Name: ast.NewIdent(addonMainPackage)}}, d.file.Decls...)
		}
		param, resources, extra := addonPackage(root, d.file.PackageName(), d.path)
		return addonCompile{pkgs: stdlibOnly, extra: extra + addonOutputCUE, files: withParam(param, resources...), output: "output"}, "", true
	case addonKindResource:
		if d.file.PackageName() == addonMainPackage {
			main, ok := addonFile(filepath.Join(root, addonTemplateFile), addonMainPackage, true)
			if !ok {
				return addonCompile{}, mainTemplateMissing, false
			}
			param, resources, extra := addonPackage(root, addonMainPackage, d.path)
			return addonCompile{pkgs: stdlibOnly, extra: extra + addonOutputCUE, files: withParam(param, append(resources, main)...), output: "output"}, "", true
		}
		pkg := d.file.PackageName()
		if pkg == "" {
			pkg = addonMainPackage
		}
		// Only parameter.cue joins a component's file: other resources do not.
		param, _, extra := addonPackage(root, pkg, d.path)
		return addonCompile{pkgs: stdlibOnly, extra: extra + componentCUE, files: withParam(param), output: "velaAddonComponent"}, "", true
	case addonKindConfig:
		extra := configTemplateCUE + secretSchema()
		if d.declares([]string{"template", parameterLabel}) {
			extra += "#velaAddonParameter: template.parameter\n"
		}
		return addonCompile{pkgs: configPackages, extra: extra}, "", true
	case addonKindView:
		return addonCompile{pkgs: workflowPackages}, "", true
	case addonKindNotes:
		param, _, _ := addonPackage(root, addonMainPackage, d.path)
		extra := addonMetaCUE + "context: {\n\tmetadata?: #velaAddonMeta\n\tinstaller: {...}\n}\n"
		if param != nil {
			extra += "#velaAddonParameter: parameter\n"
		}
		return addonCompile{pkgs: stdlibOnly, extra: extra, files: withParam(param)}, "", true
	}
	return addonCompile{pkgs: stdlibOnly}, "", true
}

// compileAddon builds the parsed document as c says.
func (d *document) compileAddon(c addonCompile) (cue.Value, []Diagnostic) {
	return d.build(cuecontext.New(), c.pkgs.imports(), c.extra, c.files...)
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
	c, _, _ := d.addonCompile(root, addonKindTemplate)
	v, diags := d.compileAddon(c)
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
				pos := valuePos(it.Value())
				if !pos.IsValid() || pos.Filename() != d.path {
					pos = d.fieldPos([]string{"output"})
				}
				diags = append(diags, d.at(pos, fmt.Sprintf("output.spec.%s[%d] has no %s", where, i, field)))
			}
			if where == "components" {
				traits, _ := it.Value().LookupPath(cue.ParsePath("traits")).List()
				for j := 0; traits.Next(); j++ {
					if !traits.Value().LookupPath(cue.ParsePath("type")).Exists() {
						diags = append(diags, d.at(valuePos(traits.Value()), fmt.Sprintf("output.spec.components[%d].traits[%d] has no type", i, j)))
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
// parameter.cue, if there is one, and the CUE under resources/ of the package,
// less the file at self. extra is the context they are given.
func addonPackage(root, pkg, self string) (param *ast.File, resources []*ast.File, extra string) {
	extra = addonContextCUE + addonMetaCUE + addonApplicationCUE
	paramPath := filepath.Join(root, addonParameterFile)
	if _, err := os.Stat(paramPath); err != nil {
		paramPath = filepath.Join(root, addonResourcesDir, addonParameterFile)
	}
	// pkg/addon always gives parameter the enable's arguments, among them
	// the clusters --clusters names, which no parameter.cue need declare.
	if f, ok := addonFile(paramPath, pkg, true); ok {
		param = f
		extra += "parameter: clusters?: [...string]\n#velaAddonParameter: parameter\n"
	} else {
		extra += "parameter: {...}\n"
	}
	var paths []string
	_ = filepath.WalkDir(filepath.Join(root, addonResourcesDir), func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && filepath.Ext(p) == cueExt {
			paths = append(paths, p)
		}
		return nil
	})
	for _, r := range paths {
		if r == filepath.Join(root, addonResourcesDir, addonParameterFile) || r == self {
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
	f, err := parser.ParseFile(path, src, parser.ParseComments)
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
	c, _, _ := d.addonCompile("", addonKindConfig)
	v, diags := d.compileAddon(c)
	diags = ignoredKeys(diags, "metadata.")
	if !v.Exists() {
		return diags
	}
	diags = append(diags, d.requireFields("a config template is read from metadata and template", "metadata", "template")...)
	if d.declares([]string{"template"}) {
		diags = append(diags, d.requireFields("it is the form a config is created from", "template.parameter")...)
	}
	return diags
}

// checkView checks a VelaQL view: it compiles with the workflow packages.
// A query returns status unless it names another field, or the view's
// export names one, which must exist.
func (d *document) checkView() []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	c, _, _ := d.addonCompile("", addonKindView)
	v, diags := d.compileAddon(c)
	if !v.Exists() {
		return diags
	}
	if export := v.LookupPath(cue.ParsePath("export")); export.Exists() {
		name, err := export.String()
		switch {
		case err != nil:
			diags = append(diags, d.at(d.fieldPos([]string{"export"}), "export names the field a query returns: it is a string"))
		case !v.LookupPath(cue.ParsePath(name)).Exists():
			diags = append(diags, d.at(d.fieldPos([]string{"export"}), fmt.Sprintf("export names %s, which the view does not set", name)))
		}
		return diags
	}
	if len(diags) == 0 && !v.LookupPath(cue.ParsePath("status")).Exists() {
		diag := d.firstLine("the view has no status, which a query returns unless it names another field, as in view{...}.result")
		diag.Severity = SeverityInfo
		diags = append(diags, diag)
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
	f, err := d.extractYAML()
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
	f, err := d.extractYAML()
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
	return append(diags, ignoredKeys(d.fromErrors(schema.Unify(data).Validate(), "#metadata"), "")...)
}

// ignoredKeys turns each unknown key under prefix, at any depth, into a
// warning: the Go type KubeVela decodes it into drops a key it does not
// know, so it does nothing rather than fails.
func ignoredKeys(diags []Diagnostic, prefix string) []Diagnostic {
	for i, diag := range diags {
		key, ok := strings.CutSuffix(diag.Message, ": field not allowed")
		if ok && strings.HasPrefix(key, prefix) {
			diags[i].Severity = SeverityWarning
			diags[i].Message = "KubeVela does not read " + key + ": it is ignored"
		}
	}
	return diags
}

// checkObjectsYAML checks a stream of Kubernetes objects: each names its
// apiVersion, kind and name, and matches its kind's schema where one is
// known.
func (d *document) checkObjectsYAML() []Diagnostic {
	f, err := d.extractYAML()
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
	if d.file.PackageName() != addonMainPackage && !d.declares([]string{"output"}) {
		return []Diagnostic{d.warnFirstLine("this file has no output: KubeVela renders a component from each resources/*.cue outside package main, and skips one without")}
	}
	c, why, ok := d.addonCompile(root, addonKindResource)
	if !ok {
		return []Diagnostic{d.warnFirstLine(why)}
	}
	v, diags := d.compileAddon(c)
	if c.output == "output" {
		return diags
	}
	for i := range diags {
		diags[i].Message = strings.ReplaceAll(diags[i].Message, "velaAddonComponent.", "output.")
	}
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

// checkTemplateYAML checks template.yaml as pkg/addon decodes it: an
// Application, beside no template.cue.
func (d *document) checkTemplateYAML(root string) []Diagnostic {
	var diags []Diagnostic
	if _, err := os.Stat(filepath.Join(root, addonTemplateFile)); err == nil {
		diags = append(diags, d.firstLine("an addon has template.cue or template.yaml, not both: KubeVela will not enable it"))
	}
	f, err := d.extractYAML()
	if err != nil {
		return append(diags, d.fromErrors(err, "")...)
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return append(diags, d.fromErrors(data.Err(), "")...)
	}
	schema := ctx.CompileString(addonApplicationCUE + "#output: #addonApplication & {\n\tapiVersion?: \"core.oam.dev/v1beta1\"\n\tkind?: \"Application\"\n}\n").LookupPath(cue.ParsePath("#output"))
	diags = append(diags, d.fromErrors(schema.Unify(data).Validate(), "#output")...)
	return append(diags, d.checkTyped(data.LookupPath(cue.ParsePath("spec")))...)
}

// checkAddonParameter checks an addon's parameter.cue: it compiles and
// declares parameter, which the addon's form is generated from.
func (d *document) checkAddonParameter() []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return diags
	}
	_, diags := d.build(cuecontext.New(), nil, "")
	return append(diags, d.requireFields("the addon's parameters, and its form, are read from it", parameterLabel)...)
}

// checkNotes checks NOTES.cue as an addon's install renders it: with its
// parameter and the installer's context, its notes a string. A failure
// there is only logged, so each problem is a warning.
func (d *document) checkNotes(root string) []Diagnostic {
	if diags, ok := d.parse(); !ok {
		return asWarnings(diags)
	}
	c, _, _ := d.addonCompile(root, addonKindNotes)
	v, diags := d.compileAddon(c)
	if v.Exists() {
		// What the installer passes is known only at install, so an
		// interpolation of it is incomplete here, not wrong.
		notes := v.LookupPath(cue.ParsePath("notes"))
		if !notes.Exists() || (notes.Err() == nil && notes.IncompleteKind() != cue.StringKind) {
			diags = append(diags, d.firstLine("notes must be a string: it is what an install prints"))
		}
	}
	return asWarnings(diags)
}

func asWarnings(diags []Diagnostic) []Diagnostic {
	for i := range diags {
		diags[i].Severity = SeverityWarning
	}
	return diags
}

// extractYAML reads the document as YAML, without its null values: the Go
// types KubeVela decodes into read a null as the zero value, as if absent.
func (d *document) extractYAML() (*ast.File, error) {
	f, err := cueyaml.Extract(d.path, d.src)
	if err != nil {
		return nil, err
	}
	astutil.Apply(f, func(c astutil.Cursor) bool {
		if field, ok := c.Node().(*ast.Field); ok {
			if lit, ok := field.Value.(*ast.BasicLit); ok && lit.Kind == token.NULL {
				c.Delete()
			}
		}
		return true
	}, nil)
	return f, nil
}
