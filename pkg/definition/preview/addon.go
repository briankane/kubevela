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

package preview

import (
	"fmt"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/addon"
	"github.com/oam-dev/kubevela/pkg/definition/analysis"
)

// addonPreviewed is the addon whose render a preview of path shows: for its
// template.cue, template.yaml or parameter.cue.
func addonPreviewed(path string) (string, bool) {
	root, ok := analysis.AddonRoot(path)
	if !ok {
		return "", false
	}
	switch rel, _ := filepath.Rel(root, path); rel {
	case addon.AppTemplateCueFileName, addon.TemplateFileName, addon.GlobalParameterFileName:
		return root, true
	}
	return "", false
}

// loadAddon loads the addon at root, with the file at path as src rather than
// as saved.
func loadAddon(root, path string, src []byte) (*addon.InstallPackage, error) {
	pkg, err := addon.LoadLocalInstallPackage(filepath.Base(root), root)
	if err != nil {
		return nil, err
	}
	switch filepath.Base(path) {
	case addon.AppTemplateCueFileName:
		pkg.AppCueTemplate.Data = string(src)
	case addon.GlobalParameterFileName:
		pkg.Parameters = string(src)
	case addon.TemplateFileName:
		app := &v1beta1.Application{}
		if err := yaml.Unmarshal(src, app); err != nil {
			return nil, err
		}
		pkg.AppTemplate = app
	}
	return pkg, nil
}

// renderAddon renders an addon as enabling it would: its Application and the
// objects installed beside it, with the values' parameter as the enable's
// arguments.
func renderAddon(root string, req Request) Result {
	res := Result{Type: "addon", Objects: []Object{}}
	pkg, err := loadAddon(root, req.Path, req.Source)
	if err != nil {
		res.fail(err)
		return res
	}
	var v struct {
		Parameter map[string]interface{} `json:"parameter"`
	}
	if err := yaml.Unmarshal(req.Values, &v); err != nil {
		res.fail(fmt.Errorf("values: %w", err))
		return res
	}
	app, aux, err := addon.RenderAppOffline(pkg, v.Parameter)
	if err != nil {
		res.fail(err)
		return res
	}
	add := func(name string, obj interface{}) {
		out, err := yaml.Marshal(obj)
		if err != nil {
			res.fail(err)
			return
		}
		res.Objects = append(res.Objects, Object{Name: name, YAML: string(out)})
	}
	add("Application "+app.Name, app)
	for _, o := range aux {
		add(o.GetKind()+" "+o.GetName(), o.Object)
	}
	return res
}

// addonSkeleton is a values file for the addon at root: each parameter its
// parameter.cue declares, required ones named, defaults filled in.
func addonSkeleton(root, path string, src []byte) (string, error) {
	pkg, err := loadAddon(root, path, src)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Values to preview the %s addon with: its parameters, as `vela addon enable` takes them. Nothing here is applied.\nparameter:", pkg.Name)
	val := cuecontext.New().CompileString(pkg.Parameters)
	if p := val.LookupPath(cue.ParsePath("parameter")); val.Err() == nil && p.Exists() && writeFields(&b, p, "  ") {
		b.WriteString("\n")
	} else {
		b.WriteString(" {}\n")
	}
	return b.String(), nil
}
