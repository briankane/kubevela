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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const rootNamesDoc = `import (
	"strings"
	kube "vela/kube"
)

"web": {
	type: "component"
	attributes: workload: type: "autodetects.core.oam.dev"
}
template: {
	_labels: {app: "web"}
	let config = parameter
	#Port: int
	output: {
		apiVersion: "example.com/v1"
		kind:       "Thing"
		spec: x: CURSOR
	}
	parameter: {}
}
`

// rootNames completes typed at the cursor of rootNamesDoc.
func rootNames(typed string) []string {
	at := strings.Index(rootNamesDoc, "CURSOR")
	doc := strings.Replace(rootNamesDoc, "CURSOR", typed, 1)
	var out []string
	for _, c := range CompleteRootName(doc, at+len(typed)) {
		out = append(out, c.Label)
	}
	return out
}

func TestCompleteRootName(t *testing.T) {
	assert.Equal(t, []string{"context", "config"}, rootNames("con"), "only what begins with con")
	assert.Equal(t, []string{"parameter"}, rootNames("par"))
	assert.Equal(t, []string{"kube"}, rootNames("ku"), "an import, by its alias")
	assert.Equal(t, []string{"strings"}, rootNames("str"))
	assert.Equal(t, []string{"_labels"}, rootNames("_l"))
	assert.Equal(t, []string{"#Port"}, rootNames("#P"))
	assert.Empty(t, rootNames(""), "nothing until a name is begun")

	// Where a label is written, a reference does not go.
	doc := strings.Replace(rootNamesDoc, "\t\tspec: x: CURSOR\n", "\t\tcon\n", 1)
	assert.Empty(t, CompleteRootName(doc, strings.Index(doc, "\t\tcon\n")+5))
	// Nor after a dot, which completes a field.
	doc = strings.Replace(rootNamesDoc, "CURSOR", "parameter.con", 1)
	assert.Empty(t, CompleteRootName(doc, strings.Index(doc, "parameter.con")+len("parameter.con")))
	// In an interpolation, it does.
	doc = strings.Replace(rootNamesDoc, "CURSOR", `"\\(con`, 1)
	var labels []string
	for _, c := range CompleteRootName(doc, strings.Index(doc, `\\(con`)+5) {
		labels = append(labels, c.Label)
	}
	assert.Contains(t, labels, "context")
}
