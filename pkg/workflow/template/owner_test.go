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

package template

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/kubevela/pkg/cue/cuex"
	wftemplate "github.com/kubevela/workflow/pkg/tasks/template"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// A step from a definition is compiled as that definition, writer included; an
// embedded step as none.
func TestLoadWithOwner(t *testing.T) {
	cli := &test.MockClient{
		MockGet: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
			o, ok := obj.(*v1beta1.WorkflowStepDefinition)
			if !ok {
				return nil
			}
			js, err := yaml.YAMLToJSON([]byte(stepDefYaml))
			if err != nil {
				return err
			}
			if err := json.Unmarshal(js, o); err != nil {
				return err
			}
			o.Annotations["cue.oam.dev/writer"] = "alice"
			return nil
		},
	}
	loader, ok := NewWorkflowStepTemplateLoader(cli, map[string]string{}).(wftemplate.OwnerLoader)
	require.True(t, ok)

	_, owner, err := loader.LoadTemplateWithOwner(context.Background(), "apply-oam-component")
	require.NoError(t, err)
	require.Equal(t, &cuex.Owner{Kind: "WorkflowStepDefinition", Namespace: "vela-system", Name: "apply-oam-component",
		Writer: &cuex.Writer{User: "alice"}}, owner)

	_, owner, err = loader.LoadTemplateWithOwner(context.Background(), "builtin-apply-component")
	require.NoError(t, err)
	require.Nil(t, owner)
}
