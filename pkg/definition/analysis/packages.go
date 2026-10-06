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

package analysis

import (
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	cuexruntime "github.com/kubevela/pkg/cue/cuex/runtime"

	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	"github.com/oam-dev/kubevela/pkg/workflow/providers"
)

// packageSet is the vela/* packages one definition type can import. Only the
// built-in packages are used: an editor must not read Package CRs from
// whichever cluster the kubeconfig happens to point at.
type packageSet struct {
	once   sync.Once
	list   func() []cuexruntime.Package
	pm     *cuexruntime.PackageManager
	mu     sync.Mutex
	values map[string]cue.Value
}

var (
	workloadPackages = &packageSet{list: velacuex.WorkloadPackages}
	sourcePackages   = &packageSet{list: velacuex.SourcePackages}
	workflowPackages = &packageSet{list: providers.WorkflowPackages}
)

// packagesFor is the set the controller compiles a definition type with.
func packagesFor(defType string) *packageSet {
	switch defType {
	case componentType, traitType:
		return workloadPackages
	case sourceType:
		return sourcePackages
	}
	return workflowPackages
}

func (s *packageSet) manager() *cuexruntime.PackageManager {
	s.once.Do(func() {
		s.pm = cuexruntime.NewPackageManager()
		s.pm.LoadInternalPackages(s.list()...)
		s.values = map[string]cue.Value{}
	})
	return s.pm
}

func (s *packageSet) imports() []*build.Instance {
	return s.manager().GetImports()
}

// Packages lists the packages in the set.
func (s *packageSet) Packages() []cuexruntime.Package {
	return s.manager().GetPackages()
}

// value is the compiled package at an import path, if the set has it.
func (s *packageSet) value(path string) (cue.Value, bool) {
	s.manager()
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.values[path]; ok {
		return v, true
	}
	for _, inst := range s.imports() {
		if inst.ImportPath == path {
			v := cuecontext.New().BuildInstance(inst)
			s.values[path] = v
			return v, true
		}
	}
	return cue.Value{}, false
}
