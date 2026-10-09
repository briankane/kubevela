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

package preview

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	cueyaml "cuelang.org/go/encoding/yaml"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// TestCases are test cases, CUE for a definition's _test.cue, that expect
// what the definition in req renders with req.Values: a case for each input
// of the values file, named name, or name and the input's name when there
// are several. Each states its parameter, its context and, for a trait, its
// workload, so it renders as the preview did.
func TestCases(ctx context.Context, req Request, name string) (string, error) {
	tmpl, ok := analysis.TemplateSource(req.Path, req.Source)
	if !ok {
		return "", fmt.Errorf("%s is not a definition, or does not parse", req.Path)
	}
	fn := map[string]string{"component": "ComponentRender", "trait": "TraitRender"}[tmpl.Type]
	if fn == "" {
		return "", fmt.Errorf("a %s definition's output is not tested by rendering: only components and traits are", tmpl.Type)
	}
	docs, err := splitDocuments(req.Values)
	if err != nil {
		return "", fmt.Errorf("values: %w", err)
	}
	if len(docs) == 0 {
		docs = [][]byte{nil}
	}
	stem := strings.TrimSuffix(filepath.Base(req.Path), filepath.Ext(req.Path))
	cctx := cuecontext.New()
	var b strings.Builder
	for i, doc := range docs {
		var v values
		if err := yaml.Unmarshal(doc, &v); err != nil {
			return "", fmt.Errorf("values: %w", err)
		}
		caseName := name
		input := v.Name
		if input == "" {
			input = strconv.Itoa(i + 1)
		}
		if len(docs) > 1 {
			caseName = fmt.Sprintf("%s (%s)", name, input)
		}
		r := render(ctx, Request{Path: req.Path, Source: req.Source, Values: doc})
		if r.Error != "" {
			return "", fmt.Errorf("it does not render with input %s: %s", input, r.Error)
		}
		c := cctx.CompileString("{}")
		c = c.FillPath(cue.ParsePath("definition"), stem)
		c = c.FillPath(cue.ParsePath("context"), map[string]string{
			"name":      v.Context.name(tmpl.Name),
			"appName":   orDefault(v.Context.AppName, defaultAppName),
			"namespace": orDefault(v.Context.Namespace, defaultNamespace),
		})
		given, err := yamlValue(cctx, "values.yaml", doc)
		if err != nil {
			return "", err
		}
		if p := given.LookupPath(cue.ParsePath("parameter")); p.Exists() {
			c = c.FillPath(cue.ParsePath("parameter"), p)
		}
		if tmpl.Type == "trait" {
			if w := given.LookupPath(cue.ParsePath("workload")); w.Exists() {
				c = c.FillPath(cue.ParsePath("workload"), w)
			} else {
				c = c.FillPath(cue.ParsePath("workload"), sampleWorkload(v.Context.name(tmpl.Name)))
			}
			if r.Workload != nil {
				if c, err = fillObject(cctx, c, "expect.output", r.Workload.YAML); err != nil {
					return "", err
				}
			}
		}
		for _, o := range r.Objects {
			at := "expect." + o.Name
			if strings.HasPrefix(o.Name, "outputs.") {
				at = "expect.outputs." + strconv.Quote(strings.TrimPrefix(o.Name, "outputs."))
			}
			if c, err = fillObject(cctx, c, at, o.YAML); err != nil {
				return "", err
			}
		}
		if err := c.Err(); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n%s: test.#%s & {\n", strconv.Quote(caseName), fn)
		for _, field := range []string{"definition", "context", "parameter", "workload", "expect"} {
			part := c.LookupPath(cue.ParsePath(field))
			if !part.Exists() {
				continue
			}
			text, err := format.Node(part.Syntax(cue.Final(), cue.Concrete(true)))
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "%s: %s\n", field, text)
		}
		b.WriteString("}\n")
	}
	out, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// yamlValue is YAML as a CUE value, its integers kept integers.
func yamlValue(cctx *cue.Context, name string, src []byte) (cue.Value, error) {
	if len(strings.TrimSpace(string(src))) == 0 {
		return cctx.CompileString("{}"), nil
	}
	f, err := cueyaml.Extract(name, src)
	if err != nil {
		return cue.Value{}, err
	}
	v := cctx.BuildFile(f)
	return v, v.Err()
}

// fillObject sets a rendered object, YAML, at a path of the case.
func fillObject(cctx *cue.Context, c cue.Value, at, src string) (cue.Value, error) {
	o, err := yamlValue(cctx, "rendered.yaml", []byte(src))
	if err != nil {
		return c, err
	}
	return c.FillPath(cue.ParsePath(at), o), nil
}
