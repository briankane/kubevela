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

package sources

import (
	"encoding/json"
	"fmt"

	cueformat "cuelang.org/go/cue/format"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	cueparser "cuelang.org/go/cue/parser"
	"k8s.io/utils/lru"
)

func (r *sourceResolver) validateResolvedOutput(sourceType, sourceTemplate string, output map[string]interface{}) error {
	schemaExpr := r.sourceSchemas[sourceType]
	if schemaExpr == "" {
		extracted, err := extractSourceSchemaExpr(sourceTemplate)
		if err != nil {
			return err
		}
		if extracted == "" {
			return nil
		}
		schemaExpr = extracted
		r.sourceSchemas[sourceType] = extracted
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	v := cuecontext.New().CompileString(fmt.Sprintf("schema: %s\noutput: close(schema) & %s", schemaExpr, string(raw)))
	if v.Err() != nil {
		return v.Err()
	}
	out := v.LookupPath(cue.ParsePath("output"))
	if !out.Exists() {
		return fmt.Errorf("source output missing")
	}
	return out.Validate(cue.Concrete(true))
}

// schemaExprCacheSize bounds the number of distinct definition templates kept.
// Unbounded, every edit to every SourceDefinition would retain another copy of
// its whole template for the life of the process.
const schemaExprCacheSize = 512

// schemaExprCache memoises the extracted schema block, which is fixed for the
// life of a definition. A pure function of the template, so the text is the key.
//
// Keyed on the text rather than a hash of it, and bounded, for the same reasons
// policyCache is.
var schemaExprCache = lru.New(schemaExprCacheSize)

func extractSourceSchemaExpr(template string) (string, error) {
	if hit, ok := schemaExprCache.Get(template); ok {
		if expr, ok := hit.(string); ok {
			return expr, nil
		}
	}
	expr, err := parseSourceSchemaExpr(template)
	if err != nil {
		// Not cached: a template that fails to parse is a condition worth
		// reporting again rather than remembering.
		return "", err
	}
	schemaExprCache.Add(template, expr)
	return expr, nil
}

func parseSourceSchemaExpr(template string) (string, error) {
	file, err := cueparser.ParseFile("-", template, cueparser.ParseComments)
	if err != nil {
		return "", err
	}
	for _, decl := range file.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		name, _, err := ast.LabelName(field.Label)
		if err != nil || name != "schema" {
			continue
		}
		bt, err := cueformat.Node(field.Value)
		if err != nil {
			return "", err
		}
		return string(bt), nil
	}
	return "", nil
}
