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

package addon

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v32/github"
	gitlab "gitlab.com/gitlab-org/api/client-go"

	velaerrors "github.com/oam-dev/kubevela/pkg/utils/errors"
)

// asNotFound recognises a client library's "no such file" and rewrites it as
// ErrFileNotFound, leaving every other error alone.
//
// Each backend reports absence in its own type, and the readers passed those
// through unwrapped, so a caller could not tell a missing file from an
// unreachable registry without matching on message text. Every error carries an
// HTTP response somewhere; this reaches for it rather than for the wording.
//
// Errors that are not a 404 are returned unchanged, deliberately: a 403 is a
// misconfigured token and papering over it as absence would turn a fixable
// problem into a silently empty result.
func asNotFound(err error, path string) error {
	if err == nil || errors.Is(err, ErrFileNotFound) {
		return err
	}
	if statusOf(err) == http.StatusNotFound {
		return fmt.Errorf("reading %q: %w", path, ErrFileNotFound)
	}
	return err
}

// statusOf digs the HTTP status out of whatever the backend returned, or 0.
func statusOf(err error) int {
	var gh *github.ErrorResponse
	if errors.As(err, &gh) && gh.Response != nil {
		return gh.Response.StatusCode
	}
	var gl *gitlab.ErrorResponse
	if errors.As(err, &gl) && gl.Response != nil {
		return gl.Response.StatusCode
	}
	// Anything else exposing a status, including readers that wrap their own.
	var coded interface{ StatusCode() int }
	if errors.As(err, &coded) {
		return coded.StatusCode()
	}
	return 0
}

// ErrFileNotFound is re-exported so addon callers need not reach for the shared
// package; it is the same value, so errors.Is matches either name.
var ErrFileNotFound = velaerrors.ErrFileNotFound
