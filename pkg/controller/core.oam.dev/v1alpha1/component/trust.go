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
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
	"github.com/oam-dev/kubevela/pkg/componentdispatch"
	"github.com/oam-dev/kubevela/pkg/monitor/metrics"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

const defaultTrustInterval = time.Minute

// SecretKeySource reads the keys this cluster trusts from the trust Secret.
type SecretKeySource struct {
	Reader    client.Reader
	Namespace string
}

// TrustedKeys returns the trusted keys; none if the hub has not pushed them yet.
func (s SecretKeySource) TrustedKeys(ctx context.Context) (componentdispatch.TrustedKeys, error) {
	secret, err := s.secret(ctx)
	if secret == nil || err != nil {
		return componentdispatch.TrustedKeys{}, err
	}
	return componentdispatch.TrustedKeysFromSecret(secret)
}

// SealingKeys returns the hub's sealing keys; none if the hub has not pushed them yet.
func (s SecretKeySource) SealingKeys(ctx context.Context) (componentdispatch.SealingKeys, error) {
	secret, err := s.secret(ctx)
	if secret == nil || err != nil {
		return componentdispatch.SealingKeys{}, err
	}
	return componentdispatch.SealingKeysFromSecret(secret)
}

// secret is the trust Secret, nil if there is none yet.
func (s SecretKeySource) secret(ctx context.Context) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: componentdispatch.TrustSecret}, secret)
	if kerrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return secret, nil
}

// TrustPusher keeps every cluster's trust Secret equal to the public half of the hub's
// signing keys, reverting anything else written there.
type TrustPusher struct {
	// Client writes to each cluster through the multicluster context.
	Client client.Client
	// Reader reads past the cache from the cluster the multicluster context names
	// (multicluster.NewClusterReader), not a manager's APIReader, which reads the hub whatever
	// cluster the context names.
	Reader    client.Reader
	Namespace string
	// Clusters lists the joined clusters; the local cluster is always included.
	Clusters func(ctx context.Context) ([]string, error)
	Recorder record.EventRecorder
	Interval time.Duration
	// MaxKeyAge, when set, rotates the signing key once it has been active for longer.
	MaxKeyAge time.Duration
}

// Start pushes on every interval until ctx ends.
func (p *TrustPusher) Start(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = defaultTrustInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.Push(ctx); err != nil {
			klog.ErrorS(err, "Failed to push trusted component signing keys")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// NeedLeaderElection is true: one replica pushes.
func (p *TrustPusher) NeedLeaderElection() bool { return true }

// Push revokes a key if an operator asked for it, rotates the signing key if asked or if it is
// older than MaxKeyAge, retires keys no Component carries, then writes the trusted keys to every
// cluster and reports keys it did not issue.
func (p *TrustPusher) Push(ctx context.Context) error {
	keys := componentdispatch.ReadThrough{Client: p.Client, Reader: p.Reader}
	revoked, err := componentdispatch.RevokeIfRequested(ctx, keys, p.Namespace)
	if err != nil {
		p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeWarning, "RevokeSigningKeyFailed", "%v", err)
	}
	if revoked != "" {
		p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeNormal, "RevokedSigningKey",
			"component signing key %s revoked: no cluster trusts it from this push on, and Components it signed are signed again", revoked)
	}
	if p.MaxKeyAge > 0 {
		if err := componentdispatch.RequestRotationIfOlder(ctx, keys, p.Namespace, p.MaxKeyAge); err != nil {
			return fmt.Errorf("checking the component signing key's age: %w", err)
		}
	}
	rotated, err := componentdispatch.RotateIfRequested(ctx, keys, p.Namespace)
	if err != nil {
		return fmt.Errorf("rotating the component signing key: %w", err)
	}
	ring, err := componentdispatch.EnsureSigningKeys(ctx, keys, p.Namespace)
	if err != nil {
		return err
	}
	if rotated {
		p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeNormal, "RotatedSigningKey",
			"component signing key %s is now active; earlier keys stay trusted until no Component carries them", ring.Active)
	}
	clusters, err := p.Clusters(ctx)
	if err != nil {
		return fmt.Errorf("listing clusters: %w", err)
	}
	all := append([]string{multicluster.ClusterLocalName}, clusters...)
	var errs []error
	// Never in the pass that rotated: a reconcile that read the keys before may still sign with
	// the old one.
	if !rotated {
		retired, holders, err := p.retireUnused(ctx, keys, ring, all)
		if err != nil {
			errs = append(errs, err)
		}
		if retired {
			if ring, err = componentdispatch.EnsureSigningKeys(ctx, keys, p.Namespace); err != nil {
				return err
			}
		}
		p.reportPileUp(ring, holders, err)
	}
	metrics.ComponentSigningKeys.Set(float64(len(ring.Keys)))
	if ring.ActiveSince.IsZero() {
		metrics.ComponentSigningKeyAge.Set(0)
	} else {
		metrics.ComponentSigningKeyAge.Set(time.Since(ring.ActiveSince).Seconds())
	}
	want := componentdispatch.TrustSecretData(ring)
	pushed := true
	for _, cluster := range all {
		if err := p.pushTo(multicluster.ContextWithClusterName(ctx, cluster), cluster, want, ring.Removed); err != nil {
			pushed = false
			errs = append(errs, fmt.Errorf("cluster %s: %w", cluster, err))
		}
	}
	if pushed && len(ring.Removed) > 0 {
		if err := componentdispatch.ForgetRemovedKeys(ctx, keys, p.Namespace, ring.Removed); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retireUnused retires every key other than the active one that no Component on any cluster
// is signed with or has sealed values to, and returns the Components holding each key it keeps.
// A cluster it cannot list holds every key in use.
func (p *TrustPusher) retireUnused(ctx context.Context, keys client.Client, ring *componentdispatch.KeyRing, clusters []string) (bool, map[string][]string, error) {
	if len(ring.Keys) < 2 {
		return false, nil, nil
	}
	holders := map[string][]string{}
	for _, cluster := range clusters {
		list := &v1alpha1.ComponentList{}
		err := p.Reader.List(multicluster.ContextWithClusterName(ctx, cluster), list)
		if meta.IsNoMatchError(err) {
			continue // no Component API there, so no Components
		}
		if err != nil {
			return false, nil, fmt.Errorf("listing Components on cluster %s to retire signing keys: %w", cluster, err)
		}
		for _, c := range list.Items {
			holder := cluster + "/" + c.Namespace + "/" + c.Name
			for _, id := range []string{c.Spec.Signature.KeyID, c.Status.SealedWith} {
				if id != "" && id != ring.Active && !slices.Contains(holders[id], holder) {
					holders[id] = append(holders[id], holder)
				}
			}
		}
	}
	retired := false
	for id := range ring.Keys {
		if id == ring.Active || len(holders[id]) > 0 {
			continue
		}
		if err := componentdispatch.RetireSigningKey(ctx, keys, p.Namespace, id); err != nil {
			return retired, holders, fmt.Errorf("retiring component signing key %s: %w", id, err)
		}
		retired = true
		p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeNormal, "RetiredSigningKey",
			"component signing key %s retired: no Component carries it", id)
	}
	return retired, holders, nil
}

// maxHeldKeys is the active key and one waiting to retire: more means a rotation started before
// the last one finished.
const maxHeldKeys = 2

// reportPileUp warns when the hub holds more keys than one rotation needs, naming what holds
// each old key, or why the hub could not tell.
func (p *TrustPusher) reportPileUp(ring *componentdispatch.KeyRing, holders map[string][]string, listErr error) {
	if len(ring.Keys) <= maxHeldKeys {
		return
	}
	var held []string
	for id := range ring.Keys {
		if id == ring.Active {
			continue
		}
		switch hs := holders[id]; {
		case listErr != nil:
			held = append(held, id)
		case len(hs) > 3:
			held = append(held, fmt.Sprintf("%s (%s and %d more)", id, strings.Join(hs[:3], ", "), len(hs)-3))
		default:
			held = append(held, fmt.Sprintf("%s (%s)", id, strings.Join(hs, ", ")))
		}
	}
	sort.Strings(held)
	why := "Components still carry them"
	if listErr != nil {
		why = listErr.Error()
	}
	p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeWarning, "SigningKeysPilingUp",
		"the hub holds %d component signing keys, so an earlier rotation has not retired its key: %s: %s",
		len(ring.Keys), why, strings.Join(held, "; "))
}

func (p *TrustPusher) pushTo(ctx context.Context, cluster string, want map[string][]byte, removed map[string]bool) error {
	secret := &corev1.Secret{}
	err := p.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: componentdispatch.TrustSecret}, secret)
	if kerrors.IsNotFound(err) {
		return p.Client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: componentdispatch.TrustSecret},
			Data:       want,
		})
	}
	if err != nil {
		return err
	}
	if sameData(secret.Data, want) {
		return nil
	}
	if foreign := foreignKeys(secret.Data, want, removed); len(foreign) > 0 {
		p.Recorder.Eventf(p.signingKeysRef(), corev1.EventTypeWarning, "ForeignTrustedKey",
			"cluster %s trusted component signing keys the hub did not issue, now removed: %v", cluster, foreign)
	}
	secret.Data = want
	return p.Client.Update(ctx, secret)
}

// signingKeysRef is what trust events are recorded against: the hub's signing keys Secret.
func (p *TrustPusher) signingKeysRef() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: componentdispatch.SigningKeysSecret}}
}

func sameData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// foreignKeys lists the entries in have that are absent from, or differ in, want, other than
// those of a key the hub removed.
func foreignKeys(have, want map[string][]byte, removed map[string]bool) []string {
	var out []string
	for k, v := range have {
		w, ok := want[k]
		if !ok && removed[componentdispatch.TrustEntryKeyID(k)] {
			continue
		}
		if !ok || !bytes.Equal(v, w) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
