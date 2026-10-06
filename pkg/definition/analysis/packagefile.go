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
	"fmt"
	"path"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
	"github.com/kubevela/pkg/apis/cue/v1alpha1"
)

// packageCUE is the Package resource as its CRD declares it.
const packageCUE = `
#Package: {
	apiVersion: string
	kind:       string
	metadata: {
		// The package's name: the #provider its functions name.
		name: string
		...
	}
	spec: {
		// The path definitions import the package by, as in ext/greeter.
		path: string
		// The provider the package's functions call: each call POSTs its $params as JSON to <endpoint>/<#do>.
		provider?: {
			// How the provider is called: KubeVela calls http and https.
			protocol: "grpc" | "http" | "https"
			// The provider's URL.
			endpoint: string
			// Headers sent with each call.
			header?: [string]: string
		}
		// The package's CUE, by file name.
		templates: [string]: string
	}
}
`

// The keys a provider function is called by.
const (
	doKey       = "#do"
	providerKey = "#provider"
	paramsKey   = "$params"
)

// CheckPackageFile checks each Package resource in a YAML stream: the
// resource as its CRD declares it, and its templates as KubeVela compiles
// them into the package, each function routed to a provider that can call
// it. It is false when the stream holds no Package.
func CheckPackageFile(filePath string, src []byte, ext *Externals) ([]Diagnostic, bool) {
	d := &document{path: filePath, src: src}
	f, err := d.extractYAML()
	if err != nil {
		return nil, false
	}
	ctx := cuecontext.New()
	data := ctx.BuildFile(f)
	if data.Err() != nil {
		return nil, false
	}
	docs := []cue.Value{data}
	if it, err := data.List(); err == nil {
		docs = nil
		for it.Next() {
			docs = append(docs, it.Value())
		}
	}
	schema := ctx.CompileString(packageCUE).LookupPath(cue.ParsePath("#Package"))
	found := false
	var diags []Diagnostic
	for _, doc := range docs {
		apiVersion, _ := doc.LookupPath(cue.ParsePath("apiVersion")).String()
		kind, _ := doc.LookupPath(cue.ParsePath("kind")).String()
		if kind != "Package" || apiVersion != v1alpha1.GroupVersion.String() {
			continue
		}
		found = true
		diags = append(diags, d.checkPackage(doc, schema, ext)...)
	}
	return sortDiagnostics(firstPerPosition(diags)), found
}

// checkPackage checks one Package resource.
func (d *document) checkPackage(doc, schema cue.Value, ext *Externals) []Diagnostic {
	diags := d.fromErrors(schema.Unify(doc).Validate(), "#Package")
	name, err := doc.LookupPath(cue.ParsePath("metadata.name")).String()
	if err != nil {
		diags = append(diags, d.at(valuePos(doc), "a Package needs a metadata.name: its functions name it as their #provider"))
	}
	pathValue := doc.LookupPath(cue.ParsePath("spec.path"))
	importPath, _ := pathValue.String()
	if strings.HasPrefix(importPath, "vela/") {
		diags = append(diags, d.at(valuePos(pathValue), "the vela/ import paths are KubeVela's own: give the package another, such as ext/"+path.Base(importPath)))
	}
	protocol := doc.LookupPath(cue.ParsePath("spec.provider.protocol"))
	if p, _ := protocol.String(); p == string(v1alpha1.ProtocolGRPC) {
		diag := d.at(valuePos(protocol), "KubeVela calls providers over http and https only, so far: a call to a grpc provider fails")
		diag.Severity = SeverityWarning
		diags = append(diags, diag)
	}
	if importPath == "" {
		return diags
	}
	hasProvider := doc.LookupPath(cue.ParsePath("spec.provider")).Exists()
	return append(diags, d.checkTemplates(doc.LookupPath(cue.ParsePath("spec.templates")), importPath, name, hasProvider, ext)...)
}

// templateFile is one of a Package's templates: its CUE, and where that
// starts in the YAML.
type templateFile struct {
	name   string
	text   string
	line   int
	indent int
}

// at is the position in the YAML of a position in the template.
func (t templateFile) at(pos token.Pos) Position {
	return Position{Line: t.line + pos.Line() - 1, Column: t.indent + pos.Column()}
}

// templateStart is where a template's CUE starts in the YAML, given its
// value's position: the line after a block scalar's indicator, at its
// indentation, or the value itself.
func (d *document) templateStart(pos token.Pos) (int, int) {
	lines := strings.Split(string(d.src), "\n")
	l := pos.Line()
	if l < 1 || l > len(lines) {
		return 1, 0
	}
	rest := strings.TrimSpace(lines[l-1][min(max(pos.Column()-1, 0), len(lines[l-1])):])
	if strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">") || strings.HasSuffix(strings.TrimSpace(lines[l-1]), "|") {
		for next := l; next < len(lines); next++ {
			if strings.TrimSpace(lines[next]) != "" {
				return next + 1, len(lines[next]) - len(strings.TrimLeft(lines[next], " "))
			}
		}
	}
	return l, pos.Column()
}

// checkTemplates compiles a Package's templates as one package, as
// KubeVela's runtime does, and checks each function it declares.
func (d *document) checkTemplates(templates cue.Value, importPath, name string, hasProvider bool, ext *Externals) []Diagnostic {
	it, err := templates.Fields()
	if err != nil {
		return nil
	}
	files := map[string]templateFile{}
	var names []string
	for it.Next() {
		text, err := it.Value().String()
		if err != nil {
			continue
		}
		t := templateFile{name: it.Selector().Unquoted(), text: text}
		t.line, t.indent = d.templateStart(valuePos(it.Value()))
		files[t.name] = t
		names = append(names, t.name)
	}
	sort.Strings(names)
	var diags []Diagnostic
	report := func(err error) {
		for _, e := range cueerrors.Errors(err) {
			format, args := e.Msg()
			msg := fmt.Sprintf(format, args...)
			if p := strings.Join(e.Path(), "."); p != "" {
				msg = p + ": " + msg
			}
			for _, pos := range append([]token.Pos{e.Position()}, e.InputPositions()...) {
				if t, ok := files[pos.Filename()]; ok && pos.IsValid() {
					start := t.at(pos)
					diags = append(diags, Diagnostic{Range: Range{Start: start, End: tokenEnd(d.src, start)}, Severity: SeverityError, Message: msg})
					break
				}
			}
		}
	}
	pkgName := path.Base(importPath)
	bi := &build.Instance{PkgName: pkgName, ImportPath: importPath}
	bi.Imports = packages{builtin: workloadPackages, ext: ext}.imports()
	for _, n := range names {
		t := files[n]
		f, err := parser.ParseFile(n, t.text, parser.ParseComments)
		if err != nil {
			report(err)
			continue
		}
		if got := f.PackageName(); got != "" && got != pkgName {
			for _, decl := range f.Decls {
				if p, ok := decl.(*ast.Package); ok {
					start := t.at(p.Name.Pos())
					diags = append(diags, Diagnostic{Range: Range{Start: start, End: tokenEnd(d.src, start)}, Severity: SeverityError,
						Message: fmt.Sprintf("the package is %s, the last part of spec.path: every template must be package %s", pkgName, pkgName)})
				}
			}
			continue
		}
		if err := bi.AddSyntax(f); err != nil {
			report(err)
		}
	}
	if len(diags) > 0 {
		return diags
	}
	v := cuecontext.New().BuildInstance(bi)
	if v.Err() != nil {
		report(v.Err())
		return diags
	}
	report(v.Validate())
	return append(diags, d.checkFunctions(v, files, name, hasProvider, ext)...)
}

// checkFunctions checks each function a package declares: a call goes to
// the package its #provider names, through that package's provider, and
// sends its $params.
func (d *document) checkFunctions(v cue.Value, files map[string]templateFile, name string, hasProvider bool, ext *Externals) []Diagnostic {
	it, err := v.Fields(cue.Definitions(true))
	if err != nil {
		return nil
	}
	known := map[string]bool{name: true}
	for _, p := range (packages{builtin: workloadPackages, ext: ext}).list() {
		known[p.GetName()] = true
	}
	var diags []Diagnostic
	at := func(pos token.Pos, severity Severity, msg string) {
		if t, ok := files[pos.Filename()]; ok && pos.IsValid() {
			start := t.at(pos)
			diags = append(diags, Diagnostic{Range: Range{Start: start, End: tokenEnd(d.src, start)}, Severity: severity, Message: msg})
		}
	}
	for it.Next() {
		fn := it.Value()
		do := fn.LookupPath(cue.MakePath(cue.Def(doKey)))
		if !do.Exists() {
			continue
		}
		label := it.Selector().String()
		provider := fn.LookupPath(cue.MakePath(cue.Def(providerKey)))
		prd, _ := provider.String()
		switch {
		case !known[prd]:
			at(provider.Pos(), SeverityWarning, fmt.Sprintf("no package is named %s: a call goes to the package its #provider names, and this one is %s", prd, name))
		case prd == name && !hasProvider:
			at(provider.Pos(), SeverityError, fmt.Sprintf("%s calls this package's provider, which it has none of: set spec.provider", label))
		}
		if !fn.LookupPath(cue.ParsePath(paramsKey)).Exists() {
			at(fn.Pos(), SeverityError, fmt.Sprintf("%s has no $params: a call sends them to the provider, so declare them, even as {}", label))
		}
	}
	return diags
}

// NewPackage is a Package resource to start from: name, imported as
// importPath, with a function calling a provider over protocol, or, when
// protocol is empty, a helper of plain CUE.
func NewPackage(name, importPath, protocol string) string {
	pkg := path.Base(importPath)
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: cue.oam.dev/v1alpha1
kind: Package
metadata:
  name: %s
spec:
  # Definitions import the package by this path: import "%s".
  path: %s
`, name, importPath, importPath)
	if protocol == "" {
		fmt.Fprintf(&b, `  templates:
    %s.cue: |
      package %s

      // +usage=Joins a name and a suffix with a hyphen
      #Name: {
        name:   string
        suffix: string
        out:    name + "-" + suffix
      }
`, pkg, pkg)
		return b.String()
	}
	fmt.Fprintf(&b, `  # Each call POSTs its $params as JSON to <endpoint>/<#do>, and reads its
  # $returns from the JSON response.
  provider:
    protocol: %s
    endpoint: %s://%s.example.com
  templates:
    %s.cue: |
      package %s

      // +usage=Greets a name
      #Greet: {
        #do:       "greet"
        #provider: "%s"
        $params: {
          // +usage=Who to greet
          name: string
        }
        $returns: {
          // +usage=The greeting
          message: string
        }
      }
`, protocol, protocol, name, pkg, pkg, name)
	return b.String()
}
