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

package controllers_test

import (
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// optIn marks an Application as wanting $( ) property expressions.
//
// The suite runs the controller in opt-in mode - expressions enabled, but only
// for Applications that ask - because that is the configuration a cluster should
// actually run. Running it wide open would leave the annotation path untested and
// would read $(VAR) in every Application on the cluster, which is what the gate
// exists to avoid.
func optIn(app *v1beta1.Application) *v1beta1.Application {
	anns := app.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns[oam.AnnotationSourceExpressions] = "true"
	app.SetAnnotations(anns)
	return app
}
