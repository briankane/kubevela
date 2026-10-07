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
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const editApp = `apiVersion: core.oam.dev/v1beta1
kind: Application
metadata:
  name: shop
spec:
  components:
    - name: web
      type: webservice
      properties:
        image: nginx
        cmd:
          - |
            echo hello
            echo again

    - name: db
      type: webservice
      properties:
        image: postgres
      traits:
        - type: scaler
          properties:
            replicas: 2
`

// applyEdit applies an edit to src with its tab stops filled: a
// placeholder's text, or X.
func applyEdit(t *testing.T, src string, e AppEdit) string {
	t.Helper()
	lines := strings.SplitAfter(src, "\n")
	offset := func(p Position) int {
		n := 0
		for i := 0; i < p.Line-1; i++ {
			n += len(lines[i])
		}
		return n + p.Column - 1
	}
	text := regexp.MustCompile(`\$\{\d+:([^}]*)\}`).ReplaceAllString(e.Snippet, "$1")
	text = regexp.MustCompile(`\$\{\d+\}`).ReplaceAllString(text, "X")
	text = strings.ReplaceAll(text, "$0", "")
	return src[:offset(e.Range.Start)] + text + src[offset(e.Range.End):]
}

// lineOf is the 1-based line of the first line of src containing s.
func lineOf(src, s string) int {
	return strings.Count(src[:strings.Index(src, s)], "\n") + 1
}

func TestApplicationLenses(t *testing.T) {
	var got []string
	for _, l := range ApplicationLenses(editApp) {
		got = append(got, l.Kind+"@"+strings.TrimSpace(strings.Split(editApp, "\n")[l.Line-1]))
	}
	assert.Equal(t, []string{
		AddPolicy + "@spec:",
		AddWorkflowStep + "@spec:",
		AddComponent + "@components:",
		AddTrait + "@- name: web",
		AddTrait + "@- name: db",
	}, got)
}

func TestAddToApplication(t *testing.T) {
	opts := builtinOnly()
	cases := map[string]struct {
		src, at, kind, typ, name string
		want                     string
	}{
		"a trait to a component with none, after its block scalar": {
			src: editApp, at: "- name: web", kind: AddTrait, typ: "scaler",
			want: "            echo again\n      traits:\n        - type: scaler\n\n    - name: db",
		},
		"a trait to a component with some": {
			src: editApp, at: "- name: db", kind: AddTrait, typ: "labels",
			want: "            replicas: 2\n        - type: labels\n",
		},
		"a component after the last": {
			src: editApp, at: "components:", kind: AddComponent, typ: "webservice", name: "cache",
			want: "            replicas: 2\n    - name: cache\n      type: webservice\n      properties:\n        image: X\n",
		},
		"a workflow step, with no workflow": {
			src: editApp, at: "spec:", kind: AddWorkflowStep, typ: "suspend", name: "approve",
			want: "            replicas: 2\n  workflow:\n    steps:\n      - name: approve\n        type: suspend\n",
		},
		"a policy, to an empty list": {
			src: strings.Replace(editApp, "spec:\n", "spec:\n  policies: []\n", 1), at: "spec:", kind: AddPolicy, typ: "topology", name: "where",
			want: "  policies:\n    - name: where\n      type: topology\n",
		},
		"a component, in compact style": {
			src: "apiVersion: core.oam.dev/v1beta1\nkind: Application\nmetadata:\n  name: x\nspec:\n  components:\n  - name: a\n    type: webservice\n    properties:\n      image: a\n", at: "components:", kind: AddComponent, typ: "webservice", name: "b",
			want: "      image: a\n  - name: b\n    type: webservice\n    properties:\n      image: X\n",
		},
	}
	for name, c := range cases {
		e, err := AddToApplication(c.src, lineOf(c.src, c.at), c.kind, c.typ, c.name, opts)
		if !assert.NoError(t, err, name) {
			continue
		}
		// VS Code indents each line of a snippet by the text before it on the
		// line it goes in: an edit starting a line has none.
		assert.Equal(t, 1, e.Range.Start.Column, "%s: an edit starts its line", name)
		got := applyEdit(t, c.src, e)
		assert.Contains(t, got, c.want, "%s:\n%s", name, got)
	}

	_, err := AddToApplication(editApp, lineOf(editApp, "metadata:"), AddTrait, "scaler", "", opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "component")
}
