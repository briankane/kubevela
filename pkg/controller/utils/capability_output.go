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

package utils

import (
	"context"
	"encoding/json"

	"k8s.io/klog/v2"

	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/schema"
)

// outputSchemaData is the schema ConfigMap data describing what a template
// applies: its `output` and its `outputs` by resource name, each key present
// only where the template declares that block. Only VelaUX reads these, so a
// template they cannot be generated from stores neither and still reconciles.
func outputSchemaData(ctx context.Context, name, template string) map[string]string {
	schemas, err := schema.GenerateOutputSchemas(ctx, template)
	if err != nil {
		klog.InfoS("skipping output schemas", "definition", name, "err", err.Error())
		return nil
	}
	data := map[string]string{}
	if schemas.Output != nil {
		if b, err := json.Marshal(schemas.Output); err == nil {
			data[types.OutputSchema] = string(b)
		}
	}
	if len(schemas.Outputs) > 0 {
		if b, err := json.Marshal(schemas.Outputs); err == nil {
			data[types.OutputsSchema] = string(b)
		}
	}
	return data
}
