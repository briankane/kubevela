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

package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// MethodRevisions lists an Application's revisions. It is this server's own
// request.
const MethodRevisions = "vela/revisions"

// MethodRevision reads the Application as one of its revisions recorded it.
const MethodRevision = "vela/revision"

// MethodRollback gives an Application the spec one of its revisions
// recorded, so its workflow runs it again.
const MethodRollback = "vela/rollback"

// RevisionsParams name an Application.
type RevisionsParams struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// RevisionInfo is what a list of revisions shows of one.
type RevisionInfo struct {
	Name           string `json:"name"`
	Version        int    `json:"version"`
	Created        string `json:"created"`
	Succeeded      bool   `json:"succeeded"`
	Phase          string `json:"phase,omitempty"`
	PublishVersion string `json:"publishVersion,omitempty"`
	// Current is whether it is the Application's latest revision.
	Current bool `json:"current,omitempty"`
}

// RevisionsResult is an Application's revisions, newest first.
type RevisionsResult struct {
	Context   string         `json:"context"`
	Revisions []RevisionInfo `json:"revisions"`
}

// RevisionParams name a revision, and for MethodRollback the Application.
type RevisionParams struct {
	Namespace   string `json:"namespace"`
	Revision    string `json:"revision"`
	Application string `json:"application,omitempty"`
}

// RevisionResult is the Application as a revision recorded it, as YAML.
type RevisionResult struct {
	Context string `json:"context"`
	YAML    string `json:"yaml"`
}

// revisionNumber is the number a revision's name ends in, -v<n>.
var revisionNumber = regexp.MustCompile(`-v(\d+)$`)

// revisionInfos are an Application's revisions, newest first, the current
// one marked.
func revisionInfos(revs []v1beta1.ApplicationRevision, current string) []RevisionInfo {
	out := make([]RevisionInfo, 0, len(revs))
	for _, r := range revs {
		info := RevisionInfo{
			Name:           r.Name,
			Created:        r.CreationTimestamp.UTC().Format(time.RFC3339),
			Succeeded:      r.Status.Succeeded,
			PublishVersion: r.Annotations[oam.AnnotationPublishVersion],
			Current:        r.Name == current,
		}
		if m := revisionNumber.FindStringSubmatch(r.Name); m != nil {
			info.Version, _ = strconv.Atoi(m[1])
		}
		if r.Status.Workflow != nil {
			info.Phase = string(r.Status.Workflow.Phase)
			// A terminated workflow keeps the phase it had when it stopped.
			if r.Status.Workflow.Terminated && !r.Status.Succeeded {
				info.Phase = "terminated"
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// appliedOnly are the annotations kubectl adds, which are not the Application's own.
var appliedOnly = []string{"kubectl.kubernetes.io/last-applied-configuration"}

// revisionApplication is the Application a revision recorded, as YAML: what
// was applied, without what the cluster added to it.
func revisionApplication(rev v1beta1.ApplicationRevision) (string, error) {
	app := rev.Spec.Application
	meta := map[string]interface{}{"name": app.Name, "namespace": app.Namespace}
	if len(app.Labels) > 0 {
		meta["labels"] = app.Labels
	}
	annotations := map[string]string{}
	for k, v := range app.Annotations {
		annotations[k] = v
	}
	for _, k := range appliedOnly {
		delete(annotations, k)
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&app.Spec)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(map[string]interface{}{"apiVersion": v1beta1.SchemeGroupVersion.String(), "kind": v1beta1.ApplicationKind, "metadata": meta, "spec": spec})
	return string(out), err
}

// rolledBack is the live Application given the spec a revision recorded. An
// Application published by version is only run again for a new version, so
// it is given one naming the revision.
func rolledBack(live *unstructured.Unstructured, rev v1beta1.ApplicationRevision) (*unstructured.Unstructured, error) {
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&rev.Spec.Application.Spec)
	if err != nil {
		return nil, err
	}
	out := live.DeepCopy()
	out.Object["spec"] = spec
	if annotations := out.GetAnnotations(); annotations[oam.AnnotationPublishVersion] != "" {
		annotations[oam.AnnotationPublishVersion] = fmt.Sprintf("%s-rollback-%d", rev.Name, time.Now().Unix())
		out.SetAnnotations(annotations)
	}
	return out, nil
}

var (
	applicationsResource = v1beta1.SchemeGroupVersion.WithResource("applications")
	revisionsResource    = v1beta1.SchemeGroupVersion.WithResource("applicationrevisions")
)

// typedRevision is a revision read as its type, which decompresses a
// compressed spec as it is read.
func typedRevision(u *unstructured.Unstructured) (v1beta1.ApplicationRevision, error) {
	var rev v1beta1.ApplicationRevision
	raw, err := json.Marshal(u.Object)
	if err != nil {
		return rev, err
	}
	return rev, json.Unmarshal(raw, &rev)
}

// listRevisions reads an Application's revisions from the cluster.
func listRevisions(cfg *rest.Config, namespace, app string) ([]RevisionInfo, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTimeout)
	defer cancel()
	a, err := client.Resource(applicationsResource).Namespace(namespace).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	current, _, _ := unstructured.NestedString(a.Object, "status", "latestRevision", "name")
	list, err := client.Resource(revisionsResource).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: oam.LabelAppName + "=" + app})
	if err != nil {
		return nil, err
	}
	revs := make([]v1beta1.ApplicationRevision, 0, len(list.Items))
	for i := range list.Items {
		// Only the metadata and status are listed: the spec is left compressed.
		var rev v1beta1.ApplicationRevision
		if runtime.DefaultUnstructuredConverter.FromUnstructured(map[string]interface{}{"metadata": list.Items[i].Object["metadata"], "status": list.Items[i].Object["status"]}, &rev) == nil {
			revs = append(revs, rev)
		}
	}
	return revisionInfos(revs, current), nil
}

// readRevision reads a revision from the cluster.
func readRevision(ctx context.Context, client dynamic.Interface, namespace, name string) (v1beta1.ApplicationRevision, error) {
	u, err := client.Resource(revisionsResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return v1beta1.ApplicationRevision{}, err
	}
	return typedRevision(u)
}

// readRevisionApplication reads the Application a revision recorded, as YAML.
func readRevisionApplication(cfg *rest.Config, namespace, name string) (string, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTimeout)
	defer cancel()
	rev, err := readRevision(ctx, client, namespace, name)
	if err != nil {
		return "", err
	}
	return revisionApplication(rev)
}

// rollback gives an Application the spec one of its revisions recorded.
func rollback(cfg *rest.Config, namespace, app, revision string) error {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterTimeout)
	defer cancel()
	rev, err := readRevision(ctx, client, namespace, revision)
	if err != nil {
		return err
	}
	if rev.Spec.Application.Name != app {
		return fmt.Errorf("%s is a revision of %s, not of %s", revision, rev.Spec.Application.Name, app)
	}
	live, err := client.Resource(applicationsResource).Namespace(namespace).Get(ctx, app, metav1.GetOptions{})
	if err != nil {
		return err
	}
	out, err := rolledBack(live, rev)
	if err != nil {
		return err
	}
	_, err = client.Resource(applicationsResource).Namespace(namespace).Update(ctx, out, metav1.UpdateOptions{})
	return err
}

// revisionRequest answers the requests about an Application's revisions.
func (s *Server) revisionRequest(msg message) (interface{}, *ResponseError) {
	switch {
	case !s.clusterEnabled:
		return nil, &ResponseError{Code: CodeInvalidParams, Message: "reading the cluster is off (kubevela.readCluster)"}
	case s.cluster == nil || s.cluster.err != nil || s.cluster.revisions == nil:
		return nil, &ResponseError{Code: CodeInvalidParams, Message: "the cluster was not reached"}
	}
	fail := func(err error) *ResponseError { return &ResponseError{Code: CodeInvalidParams, Message: err.Error()} }
	if msg.Method == MethodRevisions {
		var p RevisionsParams
		if rerr := decode(msg.Params, &p); rerr != nil {
			return nil, rerr
		}
		revs, err := s.cluster.revisions(p.Namespace, p.Name)
		if err != nil {
			return nil, fail(err)
		}
		return RevisionsResult{Context: s.cluster.context, Revisions: revs}, nil
	}
	var p RevisionParams
	if rerr := decode(msg.Params, &p); rerr != nil {
		return nil, rerr
	}
	if msg.Method == MethodRollback {
		if err := s.cluster.rollback(p.Namespace, p.Application, p.Revision); err != nil {
			return nil, fail(err)
		}
		return struct{}{}, nil
	}
	out, err := s.cluster.revisionApp(p.Namespace, p.Revision)
	if err != nil {
		return nil, fail(err)
	}
	return RevisionResult{Context: s.cluster.context, YAML: out}, nil
}
