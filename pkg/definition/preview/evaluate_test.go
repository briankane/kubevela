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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluate(t *testing.T) {
	eval := func(selection string) Evaluation {
		start := strings.Index(provenanceDef, selection)
		require.GreaterOrEqual(t, start, 0, selection)
		e, err := Evaluate(context.Background(), Request{Path: "web.cue", Source: []byte(provenanceDef), Values: []byte(provenanceValues)}, start, start+len(selection))
		require.NoError(t, err)
		return e
	}
	assert.Equal(t, Evaluation{Value: `"web"`, Concrete: true}, eval(`strings.ToLower("Web")`))
	assert.Equal(t, Evaluation{Value: `"svc-web"`, Concrete: true}, eval(`"svc-" + context.name`))
	assert.Equal(t, Evaluation{Value: "80", Concrete: true}, eval("parameter.port"), "the default")
	assert.Equal(t, Evaluation{Value: "{\n\tteam: \"shop\"\n}", Concrete: true}, eval("_labels"), "a hidden field of the template")
	assert.Equal(t, Evaluation{Value: `[{
	containerPort: 80
}]`, Concrete: true}, eval("[{containerPort: parameter.port}]"), "a list in a list element's scope")

	missing := eval("parameter.image")
	assert.Equal(t, `"nginx"`, missing.Value)

	_, err := Evaluate(context.Background(), Request{Path: "web.cue", Source: []byte(provenanceDef), Values: []byte(provenanceValues)}, 0, 3)
	assert.Error(t, err, "outside the template")
}

func TestEvaluateIncomplete(t *testing.T) {
	src := []byte(provenanceDef)
	at := strings.Index(provenanceDef, "parameter.team")
	e, err := Evaluate(context.Background(), Request{Path: "web.cue", Source: src}, at, at+len("parameter.team"))
	require.NoError(t, err)
	assert.Equal(t, Evaluation{Value: "string", Concrete: false}, e, "no values set it: its type")
}
