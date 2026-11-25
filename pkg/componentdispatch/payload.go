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

package componentdispatch

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1alpha1"
)

const (
	// PayloadFormat is a gzip-compressed JSON list of the rendered resources.
	PayloadFormat = "gzip+json/v1"
	// PayloadKey is the payload Secret's data key.
	PayloadKey = "payload"
	// MaxPayloadBytes caps the compressed payload, leaving room under a Secret's 1MiB limit.
	MaxPayloadBytes = 900 * 1024
	// maxDecodedBytes caps decompression, so a small payload cannot expand without bound.
	maxDecodedBytes = 64 * 1024 * 1024
)

var (
	// ErrPayloadTooLarge is a payload over MaxPayloadBytes once compressed.
	ErrPayloadTooLarge = errors.New("component payload is too large")
	// ErrPayloadDigest is a payload whose digest differs from the signed one.
	ErrPayloadDigest = errors.New("component payload does not match its digest")
	// ErrIndexMismatch is a payload that differs from the signed resource index.
	ErrIndexMismatch = errors.New("component payload does not match its resource index")
)

// EncodePayload compresses the rendered resources and returns the data with its sha256.
func EncodePayload(objs []*unstructured.Unstructured) (data []byte, digest string, err error) {
	list := make([]map[string]interface{}, 0, len(objs))
	for _, o := range objs {
		list = append(list, o.Object)
	}
	raw, err := json.Marshal(list) // as rendered: an explicit null can matter to the apply
	if err != nil {
		return nil, "", fmt.Errorf("encoding payload: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, "", fmt.Errorf("compressing payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("compressing payload: %w", err)
	}
	if buf.Len() > MaxPayloadBytes {
		return nil, "", fmt.Errorf("%w: %dKiB compressed, over the %dKiB limit", ErrPayloadTooLarge, buf.Len()/1024, MaxPayloadBytes/1024)
	}
	return buf.Bytes(), sha256Hex(buf.Bytes()), nil
}

// DecodePayload checks data against digest and returns the resources it holds.
func DecodePayload(data []byte, digest string) ([]*unstructured.Unstructured, error) {
	if got := sha256Hex(data); got != digest {
		return nil, fmt.Errorf("%w: sha256 %s, signed %s", ErrPayloadDigest, got, digest)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompressing payload: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, maxDecodedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompressing payload: %w", err)
	}
	if len(raw) > maxDecodedBytes {
		return nil, fmt.Errorf("decompressing payload: over %dMiB", maxDecodedBytes/1024/1024)
	}
	var list []map[string]interface{}
	if err := utiljson.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decoding payload: %w", err)
	}
	objs := make([]*unstructured.Unstructured, 0, len(list))
	for _, o := range list {
		objs = append(objs, &unstructured.Unstructured{Object: o})
	}
	return objs, nil
}

// IndexEntry identifies obj and hashes its content. Role and Trait are left for the caller,
// which knows how the resource was rendered.
func IndexEntry(obj *unstructured.Unstructured) (v1alpha1.ComponentResource, error) {
	raw, err := canonicalJSON(obj.Object)
	if err != nil {
		return v1alpha1.ComponentResource{}, fmt.Errorf("hashing %s: %w", describe(obj.GetKind(), obj.GetNamespace(), obj.GetName()), err)
	}
	return v1alpha1.ComponentResource{
		APIVersion: obj.GetAPIVersion(),
		Kind:       obj.GetKind(),
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		SHA256:     sha256Hex(raw),
	}, nil
}

// CheckIndex verifies that the payload holds exactly the indexed resources, each with its
// indexed hash.
func CheckIndex(objs []*unstructured.Unstructured, index []v1alpha1.ComponentResource) error {
	want := make(map[string]string, len(index))
	for _, r := range index {
		want[identity(r.APIVersion, r.Kind, r.Namespace, r.Name)] = r.SHA256
	}
	var problems []string
	for _, o := range objs {
		entry, err := IndexEntry(o)
		if err != nil {
			return err
		}
		id := identity(entry.APIVersion, entry.Kind, entry.Namespace, entry.Name)
		sum, ok := want[id]
		switch {
		case !ok:
			problems = append(problems, describe(entry.Kind, entry.Namespace, entry.Name)+" is not in the index")
		case sum != entry.SHA256:
			problems = append(problems, describe(entry.Kind, entry.Namespace, entry.Name)+" differs from its indexed hash")
		}
		delete(want, id)
	}
	for _, r := range index {
		if _, missing := want[identity(r.APIVersion, r.Kind, r.Namespace, r.Name)]; missing {
			problems = append(problems, describe(r.Kind, r.Namespace, r.Name)+" is indexed but not in the payload")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%w: %s", ErrIndexMismatch, strings.Join(problems, "; "))
	}
	return nil
}

func identity(apiVersion, kind, namespace, name string) string {
	return apiVersion + "|" + kind + "|" + namespace + "|" + name
}

func describe(kind, namespace, name string) string {
	if namespace == "" {
		return kind + " " + name
	}
	return kind + " " + namespace + "/" + name
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
