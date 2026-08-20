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
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRegistrySourceKind pins the precedence to BuildReader's, so the type a
// refusal names is the type the registry would actually have been read from.
func TestRegistrySourceKind(t *testing.T) {
	testCases := map[string]struct {
		registry Registry
		want     string
	}{
		"git":   {registry: Registry{Git: &GitAddonSource{URL: "https://github.com/kubevela/catalog"}}, want: "git"},
		"gitee": {registry: Registry{Gitee: &GiteeAddonSource{URL: "https://gitee.com/kubevela/catalog"}}, want: "gitee"},
		"gitlab": {registry: Registry{Gitlab: &GitlabAddonSource{
			URL: "https://gitlab.com", Repo: "kubevela/catalog"}}, want: "gitlab"},
		"oss":  {registry: Registry{OSS: &OSSAddonSource{Endpoint: "oss-cn-hangzhou.aliyuncs.com"}}, want: "oss"},
		"helm": {registry: Registry{Helm: &HelmSource{URL: "https://addons.kubevela.net"}}, want: "helm"},
		"none": {registry: Registry{}, want: ""},
		// BuildReader tries OSS before Git, so a registry carrying both is an
		// OSS registry as far as every caller is concerned.
		"oss wins over git, as it does in BuildReader": {
			registry: Registry{
				OSS: &OSSAddonSource{Endpoint: "oss-cn-hangzhou.aliyuncs.com"},
				Git: &GitAddonSource{URL: "https://github.com/kubevela/catalog"},
			},
			want: "oss",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.registry.sourceKind())
		})
	}
}

func TestBuildWriter(t *testing.T) {
	testCases := map[string]struct {
		registry Registry
		wantErr  string
	}{
		// An object store has no commit semantics and BuildReader drops the
		// credential on the floor for it, so refusing by name beats failing
		// somewhere further in with something obscure.
		"oss is refused, by name": {
			registry: Registry{
				Name: "oss-reg",
				OSS:  &OSSAddonSource{Endpoint: "oss-cn-hangzhou.aliyuncs.com", Bucket: "kubevela-addons"},
			},
			wantErr: `registry "oss-reg" is of type oss, which does not support writes`,
		},
		"helm is refused, by name": {
			registry: Registry{Name: "helm-reg", Helm: &HelmSource{URL: "https://addons.kubevela.net"}},
			wantErr:  `registry "helm-reg" is of type helm, which does not support writes`,
		},
		"a registry with no source at all": {
			registry: Registry{Name: "empty"},
			wantErr:  `registry "empty" has no source to write to`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			w, err := tc.registry.BuildWriter()
			assert.Nil(t, w)
			assert.EqualError(t, err, tc.wantErr)
		})
	}
}
