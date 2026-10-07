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
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"
)

// SourceScheme is the URI scheme of the read-only documents of what KubeVela
// declares: a package's CUE, vela-source:/package/<import path>.cue, and a
// definition type's context, vela-source:/context/<type>.cue.
const SourceScheme = "vela-source"

const (
	packageSourcePrefix = SourceScheme + ":/package/"
	contextSourcePrefix = SourceScheme + ":/context/"
)

// IsSource reports whether uri is a read-only document's.
func IsSource(uri string) bool {
	return strings.HasPrefix(uri, SourceScheme+":")
}

// Source is the text of the read-only document at uri: the CUE of the
// package it names, built in or among ext, or the context of the definition
// type it names.
func Source(uri string, ext *Externals) (string, bool) {
	switch {
	case strings.HasPrefix(uri, packageSourcePrefix):
		path := strings.TrimSuffix(strings.TrimPrefix(uri, packageSourcePrefix), ".cue")
		pkg, ok := findPackage(path, ext)
		if !ok {
			return "", false
		}
		return packageSource(path, pkg.GetTemplates())
	case strings.HasPrefix(uri, contextSourcePrefix):
		return contextSource(strings.TrimSuffix(strings.TrimPrefix(uri, contextSourcePrefix), ".cue"))
	}
	return "", false
}

// findPackage is the package at path: built in, for any definition type,
// or among ext.
func findPackage(path string, ext *Externals) (cuexruntime.Package, bool) {
	for _, set := range []*packageSet{workloadPackages, workflowPackages, scopedPolicyPackages, sourcePackages} {
		for _, p := range set.Packages() {
			if p.GetPath() == path {
				return p, true
			}
		}
	}
	return ext.find(path)
}

// packageSource is a package's templates as one file: its imports first,
// then its declarations, comments kept.
func packageSource(path string, templates []string) (string, bool) {
	name := path[strings.LastIndex(path, "/")+1:]
	header := fmt.Sprintf("// The %s package, as this vela builds it in. Read only.\n\npackage %s\n", path, name)
	if !ast.IsValidIdent(name) {
		header = fmt.Sprintf("// The %s package, as this vela builds it in. Read only.\n", path)
	}
	imports := map[string]*ast.ImportSpec{}
	var decls []ast.Decl
	for _, src := range templates {
		f, err := parser.ParseFile(path, src, parser.ParseComments)
		if err != nil {
			return "", false
		}
		for _, d := range f.Decls {
			switch x := d.(type) {
			case *ast.Package:
			case *ast.ImportDecl:
				for _, spec := range x.Specs {
					imports[spec.Path.Value] = spec
				}
			default:
				decls = append(decls, d)
			}
		}
	}
	var b strings.Builder
	b.WriteString(header)
	if len(imports) > 0 {
		paths := make([]string, 0, len(imports))
		for p := range imports {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		b.WriteString("\nimport (\n")
		for _, p := range paths {
			if n := imports[p].Name; n != nil {
				b.WriteString("\t" + n.Name + " " + p + "\n")
			} else {
				b.WriteString("\t" + p + "\n")
			}
		}
		b.WriteString(")\n")
	}
	body, err := format.Node(&ast.File{Decls: decls})
	if err != nil {
		return "", false
	}
	b.WriteString("\n")
	b.Write(body)
	return b.String(), true
}

// contextSource is the context a definition type's template reads, each
// field with its type and usage, the fields the registry excludes left out.
func contextSource(defType string) (string, bool) {
	fields := ContextFields(defType)
	if fields == nil {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// The context a %s's template reads. KubeVela fills it in at render. Read only.\n\ncontext: {\n", defType)
	for _, f := range fields {
		if f.Hidden {
			continue
		}
		if f.Doc != "" {
			for _, line := range strings.Split(f.Doc, "\n") {
				b.WriteString("\t// " + line + "\n")
			}
		}
		mark := ""
		if !f.Required {
			mark = "?"
		}
		fmt.Fprintf(&b, "\t%s%s: %s\n", contextLabelText(f.Name), mark, f.Type)
	}
	b.WriteString("}\n")
	out, err := format.Source([]byte(b.String()), format.UseSpaces(0))
	if err != nil {
		return b.String(), true
	}
	return string(out), true
}

// contextLabelText is a context field's label, quoted when it must be.
func contextLabelText(name string) string {
	if ast.IsValidIdent(name) {
		return name
	}
	return strconv.Quote(name)
}

// sourceIndex parses the read-only document at uri for navigation.
func sourceIndex(uri string, ext *Externals) (*navIndex, bool) {
	text, ok := Source(uri, ext)
	if !ok {
		return nil, false
	}
	return parseNav(uri, text)
}
