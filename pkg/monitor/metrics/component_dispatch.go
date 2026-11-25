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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ComponentSigningKeys is the number of Component signing keys the hub holds: one, or two
	// while a rotation waits to retire the old key. More means retirement is held up.
	ComponentSigningKeys = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevela_component_signing_keys",
		Help: "Component signing keys the hub holds, the active one included.",
	})
	// ComponentSigningKeyAge is how long the active Component signing key has been active.
	ComponentSigningKeyAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubevela_component_signing_key_age_seconds",
		Help: "Seconds since the active Component signing key became active; 0 if the hub does not know.",
	})
)

func init() {
	for _, c := range []prometheus.Collector{ComponentSigningKeys, ComponentSigningKeyAge} {
		if err := metrics.Registry.Register(c); err != nil {
			klog.Error(err)
		}
	}
}
