/*
Copyright 2021 The KubeVela Authors.

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

package core_oam_dev

import "time"

// Args args used by controller
type Args struct {

	// RevisionLimit is the maximum number of revisions that will be maintained.
	// The default value is 50.
	RevisionLimit int

	// AppRevisionLimit is the maximum number of application revisions that will be maintained.
	// The default value is 10.
	AppRevisionLimit int

	// DefRevisionLimit is the maximum number of component/trait definition revisions that will be maintained.
	// The default value is 20.
	DefRevisionLimit int

	// ConcurrentReconciles is the concurrent reconcile number of the controller
	ConcurrentReconciles int

	// AutoGenWorkloadDefinition indicates whether automatic generated workloadDefinition which componentDefinition refers to
	AutoGenWorkloadDefinition bool

	// IgnoreAppWithoutControllerRequirement indicates that application controller will not process the app without 'app.oam.dev/controller-version-require' annotation.
	IgnoreAppWithoutControllerRequirement bool

	// IgnoreDefinitionWithoutControllerRequirement indicates that trait/component/workflowstep definition controller will not process the definition without 'definition.oam.dev/controller-version-require' annotation.
	IgnoreDefinitionWithoutControllerRequirement bool

	// UseWebhook mirrors --use-webhook: the admission webhooks are registered and the
	// manager's webhook server will start. Controllers that want to wait for that
	// server (through mgr.GetWebhookServer) must check this first: in controller-runtime
	// the first GetWebhookServer call adds the server to the manager, and without the
	// serving certificates the manager fails to start.
	UseWebhook bool

	// ComponentKeysNamespace holds the hub's component signing keys. Set when Component
	// dispatch is on; the Application controller signs what it dispatches with them.
	ComponentKeysNamespace string

	// ComponentReconcilerTimeout is how long a dispatched Component may wait for vela-agent
	// to apply its revision before its Application reports that nothing is applying it.
	ComponentReconcilerTimeout time.Duration

	// ComponentAgentRenderInterval is the least time between renders of an Application that
	// waits on vela-agent to report a dispatched Component healthy.
	ComponentAgentRenderInterval time.Duration

	// ComponentAgentHealthInterval is how often such an Application reads its Components
	// between renders.
	ComponentAgentHealthInterval time.Duration
}
