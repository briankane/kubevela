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

package application

import (
	"cuelang.org/go/cue"

	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

// ExpressionTargetError is why admission refuses a property's expression, raw,
// feeding the parameter at segs of params, or "" when it accepts it: the
// expression's own type error, a type the parameter does not take, or a read
// that may be absent feeding a required parameter unguarded. schemas maps each
// source binding to its SourceDefinition's schema text; params is the
// consuming definition's parameter, which may not exist. It is what the
// webhook checks each property with, for tools that check an Application
// before it is applied.
func ExpressionTargetError(raw string, schemas map[string]string, ctxSchema propexpr.ContextSchema,
	params cue.Value, segs []string, targetDesc string) string {
	var param *cueStruct
	if params.Exists() {
		param = &cueStruct{root: params}
	}
	msg, _ := expressionTargetError(raw, schemas, ctxSchema, param, segs, targetDesc)
	return msg
}

// SourceSchemaText is a SourceDefinition template's schema, as admission reads
// it for typing expressions, or "" when it declares none.
func SourceSchemaText(template string) (string, error) {
	return extractSourceSchemaExprForAdmission(template)
}

// TargetParameter is a definition template's parameter, compiled on its own
// as admission compiles it for typing expressions.
func TargetParameter(template string) (cue.Value, bool) {
	param, ok := parameterBlockOnly(template)
	if !ok {
		return cue.Value{}, false
	}
	return param.root, true
}
