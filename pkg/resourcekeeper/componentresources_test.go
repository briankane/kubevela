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

package resourcekeeper

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	apicommon "github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

func managed(component, name string, deleted bool) v1beta1.ManagedResource {
	return v1beta1.ManagedResource{
		ClusterObjectReference: apicommon.ClusterObjectReference{
			ObjectReference: corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Name: name, Namespace: "ns"},
		},
		OAMObjectReference: apicommon.OAMObjectReference{Component: component},
		Deleted:            deleted,
	}
}

// A component's applied resources, from both trackers that hold live ones, and
// never an entry already marked for deletion.
func TestComponentResources(t *testing.T) {
	h := &resourceKeeper{
		_currentRT: &v1beta1.ResourceTracker{Spec: v1beta1.ResourceTrackerSpec{ManagedResources: []v1beta1.ManagedResource{
			managed("db", "db-workload", false), managed("api", "api-workload", false), managed("db", "db-old", true),
		}}},
		_rootRT: &v1beta1.ResourceTracker{Spec: v1beta1.ResourceTrackerSpec{ManagedResources: []v1beta1.ManagedResource{
			managed("db", "db-shared", false),
		}}},
	}
	var names []string
	for _, mr := range h.ComponentResources("db") {
		names = append(names, mr.Name)
	}
	require.ElementsMatch(t, []string{"db-workload", "db-shared"}, names)

	require.Empty(t, (&resourceKeeper{}).ComponentResources("db"), "no tracker yet is nothing applied")
}

// A deploy step records into the tracker from parallel tasks while readers ask
// for a producer's resources; run with -race.
func TestComponentResourcesIsSafeAlongsideRecording(t *testing.T) {
	h := &resourceKeeper{_currentRT: &v1beta1.ResourceTracker{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			h.mu.Lock()
			h._currentRT.Spec.ManagedResources = append(h._currentRT.Spec.ManagedResources, managed("db", "db", false))
			h.mu.Unlock()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = h.ComponentResources("db")
	}
	<-done
	require.Len(t, h.ComponentResources("db"), 200)
}
