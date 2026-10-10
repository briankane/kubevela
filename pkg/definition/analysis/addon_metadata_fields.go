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
	"strconv"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/parser"
)

// addonMetaSections are where the addon panel shows each top-level field of
// metadata.yaml; one not named is under About.
var addonMetaSections = map[string]string{
	"deployTo":      "Deploy",
	"needNamespace": "Deploy",
	"dependencies":  "Requires",
	"system":        "Requires",
	"invisible":     "Listing",
	"tags":          "Listing",
	"uxPlugins":     "Listing",
	"annotations":   "Listing",
}

// addonMetaSkipped are fields of Meta the panel leaves out: runtime_cluster is
// the legacy spelling of runtimeCluster.
var addonMetaSkipped = map[string]bool{"deployTo.runtime_cluster": true}

// AddonMetadataFields are the fields of an addon's metadata.yaml as pkg/addon's
// Meta type has them, with its docs, for the addon panel's form.
var AddonMetadataFields = sync.OnceValue(func() []HeaderField {
	f, err := parser.ParseFile("meta.cue", addonMetaCUE, parser.ParseComments)
	if err != nil {
		return nil
	}
	meta := cuecontext.New().BuildFile(f).LookupPath(cue.ParsePath("#velaAddonMeta"))
	var out []HeaderField
	metaFields(meta, nil, &out)
	return out
})

// metaFields adds the fields of a Meta schema under path to out, a struct's
// fields one by one.
func metaFields(schema cue.Value, path []string, out *[]HeaderField) {
	it, err := schema.Fields(cue.Optional(true))
	if err != nil {
		return
	}
	for it.Next() {
		name := strings.TrimSuffix(it.Selector().String(), "?")
		if u, err := strconv.Unquote(name); err == nil {
			name = u
		}
		p := append(append([]string{}, path...), name)
		if addonMetaSkipped[strings.Join(p, ".")] {
			continue
		}
		v := it.Value()
		section := addonMetaSections[p[0]]
		if section == "" {
			section = SectionAbout
		}
		kind := kindOfSchema(v)
		if kind == "other" && v.IncompleteKind() == cue.StructKind {
			metaFields(v, p, out)
			continue
		}
		hf := HeaderField{Path: p, Kind: kind, Label: humanise(name), Doc: firstSentences(v), Section: section, Required: !it.IsOptional()}
		if len(path) > 0 {
			hf.Label = humanise(path[len(path)-1]) + ": " + strings.ToLower(hf.Label)
		}
		if kind == "list" {
			var items []HeaderField
			metaFields(v.LookupPath(cue.MakePath(cue.AnyIndex)), nil, &items)
			for i := range items {
				items[i].Section = ""
			}
			hf.Items = items
		}
		*out = append(*out, hf)
	}
}
