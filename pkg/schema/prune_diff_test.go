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

package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"github.com/stretchr/testify/require"
)

// TestParameterSchemaDiff generates the schema of every definition under
// PARAMETER_SCHEMA_DIFF_DIRS (colon separated) both ways and fails on any
// definition where the pruned schema differs from, or fails where, the
// whole-template one succeeds. PARAMETER_SCHEMA_DIFF_OUT names a file for the
// per-definition report.
func TestParameterSchemaDiff(t *testing.T) {
	dirs := os.Getenv("PARAMETER_SCHEMA_DIFF_DIRS")
	if dirs == "" {
		t.Skip("PARAMETER_SCHEMA_DIFF_DIRS not set")
	}
	type row struct {
		File, Old, New, Path, Result string
	}
	var rows []row
	counts := map[string]int{}
	ctx := context.Background()
	for _, dir := range strings.Split(dirs, ":") {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".cue") {
				return err
			}
			b, err := os.ReadFile(filepath.Clean(p))
			if err != nil {
				return err
			}
			tmpl, ok, err := definitionTemplate(string(b))
			r := row{File: p}
			switch {
			case err != nil:
				r.Result = "unparseable: " + err.Error()
			case !ok:
				return nil
			default:
				oldS, oldErr := ParsePropertiesToSchema(ctx, tmpl)
				newS, path, newErr := ParseParameterSchema(ctx, tmpl)
				r.Old, r.New, r.Path = status(oldErr), status(newErr), string(path)
				switch {
				case oldErr != nil && newErr != nil:
					r.Result = "both-fail"
				case oldErr != nil:
					r.Result = "fixed"
				case newErr != nil:
					r.Result = "regressed"
				case sameJSON(t, oldS, newS):
					r.Result = "same"
				default:
					r.Result = "differs"
				}
			}
			counts[strings.SplitN(r.Result, ":", 2)[0]]++
			counts["path:"+r.Path]++
			rows = append(rows, r)
			return nil
		})
		require.NoError(t, err)
	}

	var keys []string
	for k := range counts {
		keys = append(keys, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	sort.Strings(keys)
	t.Logf("%d definitions: %s", len(rows), strings.Join(keys, " "))

	if out := os.Getenv("PARAMETER_SCHEMA_DIFF_OUT"); out != "" {
		b, err := json.MarshalIndent(rows, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, b, 0600))
	}
	for _, r := range rows {
		if r.Result != "same" && r.Result != "fixed" {
			t.Errorf("%s: %s (old: %s, new: %s)", r.File, r.Result, r.Old, r.New)
		}
	}
}

// definitionTemplate extracts what `vela def apply` stores as the CUE
// template: the file's imports plus the body of its `template` field.
func definitionTemplate(src string) (string, bool, error) {
	f, err := parser.ParseFile("def", src, parser.ParseComments)
	if err != nil {
		return "", false, err
	}
	out := &ast.File{}
	found := false
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.ImportDecl:
			out.Decls = append(out.Decls, d)
		case *ast.Field:
			if name, _, _ := ast.LabelName(d.Label); name == "template" {
				if s, ok := d.Value.(*ast.StructLit); ok {
					out.Decls = append(out.Decls, s.Elts...)
					found = true
				}
			}
		}
	}
	if !found {
		return "", false, nil
	}
	b, err := format.Node(out)
	return string(b), true, err
}

func status(err error) string {
	if err == nil {
		return "ok"
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func sameJSON(t *testing.T, a, b any) bool {
	var x, y any
	for _, p := range []struct {
		in  any
		out *any
	}{{a, &x}, {b, &y}} {
		raw, err := json.Marshal(p.in)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, p.out))
	}
	return reflect.DeepEqual(x, y)
}
