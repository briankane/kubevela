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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
	"github.com/kubevela/pkg/apis/cue/v1alpha1"
	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Externals are the CueX packages a workspace adds to the built-in ones:
// custom providers, declared by Package resources. A definition of any type
// may import them, as the controller's compilers load them for every type.
type Externals struct {
	packages []cuexruntime.Package

	mu     sync.Mutex
	values map[string]cue.Value
	docs   map[string]cue.Value
}

// NewExternals holds the given packages.
func NewExternals(pkgs []cuexruntime.Package) *Externals {
	return &Externals{packages: pkgs, values: map[string]cue.Value{}, docs: map[string]cue.Value{}}
}

// ParsePackages reads the Package resources (cue.oam.dev/v1alpha1) in YAML
// of any number of documents; other kinds are passed over. A Package whose
// templates do not compile is reported and left out.
func ParsePackages(data []byte) ([]cuexruntime.Package, []error) {
	var pkgs []cuexruntime.Package
	var errs []error
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return pkgs, append(errs, err)
		}
		var meta struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
		}
		if yaml.Unmarshal(doc, &meta) != nil || meta.Kind != "Package" || meta.APIVersion != v1alpha1.GroupVersion.String() {
			continue
		}
		var src v1alpha1.Package
		if err := yaml.Unmarshal(doc, &src); err != nil {
			errs = append(errs, err)
			continue
		}
		pkg, err := cuexruntime.NewExternalPackage(&src)
		if err != nil {
			errs = append(errs, fmt.Errorf("package %s: %w", src.Name, err))
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, errs
}

// packages is what a definition can import: its type's built-in packages,
// then the workspace's. A built-in path wins over an external one.
type packages struct {
	builtin *packageSet
	ext     *Externals
}

func (d *document) packages() packages {
	return packages{builtin: packagesFor(d.templateKind()), ext: d.opts.Externals}
}

func (p packages) list() []cuexruntime.Package {
	out := append([]cuexruntime.Package{}, p.builtin.Packages()...)
	if p.ext == nil {
		return out
	}
	taken := map[string]bool{}
	for _, b := range out {
		taken[b.GetPath()] = true
	}
	for _, e := range p.ext.packages {
		if !taken[e.GetPath()] {
			out = append(out, e)
		}
	}
	return out
}

func (p packages) imports() []*build.Instance {
	var out []*build.Instance
	for _, pkg := range p.list() {
		out = append(out, pkg.GetImports()...)
	}
	return out
}

func (p packages) value(path string) (cue.Value, bool) {
	if v, ok := p.builtin.value(path); ok {
		return v, true
	}
	return p.ext.value(path)
}

func (p packages) documented(path string) (cue.Value, bool) {
	if v, ok := p.builtin.documented(path); ok {
		return v, true
	}
	return p.ext.documented(path)
}

func (e *Externals) find(path string) (cuexruntime.Package, bool) {
	if e == nil {
		return nil, false
	}
	for _, p := range e.packages {
		if p.GetPath() == path {
			return p, true
		}
	}
	return nil, false
}

func (e *Externals) value(path string) (cue.Value, bool) {
	pkg, ok := e.find(path)
	if !ok {
		return cue.Value{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.values[path]; ok {
		return v, true
	}
	imports := pkg.GetImports()
	if len(imports) == 0 {
		return cue.Value{}, false
	}
	v := cuecontext.New().BuildInstance(imports[0])
	e.values[path] = v
	return v, true
}

func (e *Externals) documented(path string) (cue.Value, bool) {
	pkg, ok := e.find(path)
	if !ok {
		return cue.Value{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.docs[path]; ok {
		return v, true
	}
	v, ok := documentedPackage(path, pkg.GetTemplates())
	if ok {
		e.docs[path] = v
	}
	return v, ok
}

// documentedPackage compiles a package's sources with their comments kept,
// for its members' docs.
func documentedPackage(path string, templates []string) (cue.Value, bool) {
	var decls []ast.Decl
	for _, src := range templates {
		f, err := parser.ParseFile(path, src, parser.ParseComments)
		if err != nil {
			return cue.Value{}, false
		}
		for _, d := range f.Decls {
			switch d.(type) {
			case *ast.Package, *ast.ImportDecl:
			default:
				decls = append(decls, d)
			}
		}
	}
	return cuecontext.New().BuildFile(&ast.File{Decls: decls}), true
}

// checkCustomProviders warns on each custom provider package a component or
// trait imports: its calls run on every render of every Application using
// the definition, where a slow or varying answer stalls or churns them.
func (d *document) checkCustomProviders() []Diagnostic {
	if kind := d.templateKind(); kind != componentType && kind != traitType {
		return nil
	}
	var diags []Diagnostic
	for _, decl := range d.imports {
		for _, spec := range decl.Specs {
			path := strings.Trim(spec.Path.Value, `"`)
			if _, ok := d.opts.Externals.find(path); !ok {
				continue
			}
			diag := d.at(spec.Path.Pos(), fmt.Sprintf("%s is a custom provider: its use in a %s is experimental. Its calls run on every render, so make sure they are fast and deterministic", path, d.typ))
			diag.Severity = SeverityWarning
			diags = append(diags, diag)
		}
	}
	return diags
}

// checkUnusedImports reports each import the file never refers to. The
// controller fails to render a component, trait or policy with one; the
// workflow engine and the other compilers pass it over, so there it warns.
func (d *document) checkUnusedImports() []Diagnostic {
	severity := SeverityWarning
	switch d.templateKind() {
	case componentType, traitType, policyType, workloadType:
		severity = SeverityError
	}
	used := map[*ast.ImportSpec]bool{}
	ast.Walk(d.file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if spec, ok := id.Node.(*ast.ImportSpec); ok {
				used[spec] = true
			}
		}
		return true
	}, nil)
	var diags []Diagnostic
	for _, decl := range d.imports {
		for _, spec := range decl.Specs {
			if used[spec] {
				continue
			}
			msg := "imported and not used: " + spec.Path.Value
			if spec.Name != nil {
				msg += " as " + spec.Name.Name
			}
			diag := d.at(spec.Path.Pos(), msg)
			diag.Severity = severity
			diag.Fixes = []Fix{removeImportFix(decl, spec)}
			diags = append(diags, diag)
		}
	}
	return diags
}
