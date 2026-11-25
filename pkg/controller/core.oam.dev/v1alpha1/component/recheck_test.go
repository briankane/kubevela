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

package component

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

func TestRechecksAnUnreadyComponentSoonThenLessOften(t *testing.T) {
	r := &Reconciler{HealthRecheck: 10 * time.Second}
	c := &v1alpha1.Component{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"}, Spec: v1alpha1.ComponentSpec{Revision: 1}}
	waits := func(n int, at time.Time) []time.Duration {
		var out []time.Duration
		for range n {
			w := r.recheckAfter(c, at)
			out = append(out, w)
			at = at.Add(w)
		}
		return out
	}
	s := time.Second
	start := time.Now()
	require.Equal(t, []time.Duration{s, s, 2 * s, 4 * s, 8 * s, 10 * s, 10 * s}, waits(7, start),
		"as long again as it has been unready, from 1s up to the health recheck")

	later := start.Add(time.Minute)
	c.Spec.Revision = 2
	require.Equal(t, s, r.recheckAfter(c, later), "a new revision starts again")

	r.ready(c)
	require.Equal(t, s, r.recheckAfter(c, later.Add(time.Minute)), "so does one unready again after it was ready")
}
