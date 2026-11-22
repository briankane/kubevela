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

// Package access decides whether a writer may use a definition that requires a
// role or a grant, as its spec.restrictions or annotations say.
package access

import (
	"context"
	"fmt"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubevela/pkg/cue/cuex"
	cuexaccess "github.com/kubevela/pkg/cue/cuex/access"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/pkg/auth"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
)

// VerbUse is the RBAC verb a grant on a definition gives.
const VerbUse = "use"

// Result is what a check decided about one definition.
type Result struct {
	// Refusal says what the writer lacks, empty when allowed.
	Refusal string
	// Warn says the definition only warns, so a refusal admits with a warning.
	Warn bool
}

// Check decides whether writer may use def. It reads RoleBindings and
// ClusterRoleBindings through cli, so a cached client answers from informers, and
// asks the API server for a grant with a SubjectAccessReview.
//
// Roles and grants are decided where the definition lives: only ClusterRoleBindings
// and RoleBindings in its namespace count, and the grant is asked there. A team
// admin may bind a role with no rules to themselves in their own namespace, and a
// namespace-admin role holds every verb there, so either would otherwise pass.
func Check(ctx context.Context, cli client.Client, def client.Object, writer authenticationv1.UserInfo) (Result, error) {
	r := nsrestrict.Of(def)
	if !nsrestrict.RestrictsWriters(r) {
		return Result{}, nil
	}
	var lacks []string
	if len(r.RequireRoles) > 0 {
		ok, err := Member(ctx, cli, writer, r.RequireRoles, def.GetNamespace())
		if err != nil {
			return Result{}, err
		}
		if !ok {
			lacks = append(lacks, "be a member of "+roleList(r.RequireRoles))
		}
	}
	if r.RequireGrant {
		resource := resourceOf(def)
		ok, err := auth.UserCan(ctx, cli, writer, authorizationv1.ResourceAttributes{
			Verb: VerbUse, Group: "core.oam.dev", Resource: resource,
			Name: def.GetName(), Namespace: def.GetNamespace(),
		})
		if err != nil {
			return Result{}, err
		}
		if !ok {
			lacks = append(lacks, fmt.Sprintf("hold %q on %s %q in namespace %q", VerbUse, resource, def.GetName(), def.GetNamespace()))
		}
	}
	if len(lacks) == 0 {
		return Result{}, nil
	}
	msg := fmt.Sprintf("%s %q is restricted: the writer %s must %s", nsrestrict.KindOf(def), def.GetName(),
		writer.Username, strings.Join(lacks, " and "))
	if r.Message != "" {
		msg += ". " + r.Message
	}
	return Result{Refusal: msg, Warn: r.Mode == common.RestrictionModeWarn}, nil
}

// Member reports whether writer is bound to any of roles by a ClusterRoleBinding,
// or by a RoleBinding in namespace. Membership is by name: a binding to another
// role, however wide, does not count.
func Member(ctx context.Context, cli client.Reader, writer authenticationv1.UserInfo, roles []common.RoleRef, namespace string) (bool, error) {
	return cuexaccess.Member(ctx, cli, cuex.Writer{User: writer.Username, Groups: writer.Groups}, roles, namespace)
}

func roleList(roles []common.RoleRef) string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.String()
	}
	return strings.Join(names, " or ")
}

// resourceOf is the RBAC resource a grant on def names: its kind, lower case and plural.
func resourceOf(def client.Object) string {
	return strings.ToLower(nsrestrict.KindOf(def)) + "s"
}
