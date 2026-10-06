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
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"cuelang.org/go/cue/ast"
)

// A marker is a `// +key=value` line in a field's doc comment. KubeVela's
// readers of them report nothing: a misspelt or misplaced marker is silently
// ignored. So each problem here is a warning, not an error.

// markerPlace is where in a definition file a marker is written. KubeVela
// reads markers through references, so one in a helper field or a #Definition
// of the template takes effect wherever that is used: within the template, a
// marker's place cannot rule it out.
type markerPlace int

const (
	placeOther markerPlace = iota
	// placeHeader is the definition's header, where no marker is read.
	placeHeader
	// placePatch is a field under a trait's patch or patchOutputs.
	placePatch
	// placePatchRoot is the patch or patchOutputs field itself.
	placePatchRoot
)

// Marker describes one marker: what it does and the value it takes.
type Marker struct {
	Name string
	Doc  string
	// Values are the values the marker takes, when it takes one of a set.
	Values []string
	// Bare is set for a marker that takes no value.
	Bare bool
	// check says what is wrong with a value, or "" when it is fine. value is
	// nil for a bare marker.
	check func(value *string) string
}

func required(what string) func(*string) string {
	return func(v *string) string {
		if v == nil || *v == "" {
			return "takes a value: +" + what + "=..."
		}
		return ""
	}
}

func bare(v *string) string {
	if v != nil {
		return "takes no value"
	}
	return ""
}

func bareOrTrue(v *string) string {
	if v != nil && *v != "true" {
		return "takes no value, or true"
	}
	return ""
}

func oneOf(what string, values ...string) func(*string) string {
	return func(v *string) string {
		if v == nil {
			return "takes a value: +" + what + "=" + strings.Join(values, "|")
		}
		for _, ok := range values {
			if *v == ok {
				return ""
			}
		}
		return "takes " + orList(values)
	}
}

func wholeNumber(v *string) string {
	if v == nil {
		return "takes a whole number"
	}
	if _, err := strconv.Atoi(*v); err != nil {
		return "takes a whole number"
	}
	return ""
}

func orList(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
}

// parameterMarkers describe a parameter.
var parameterMarkers = []Marker{
	{Name: "usage", Doc: "Describes the parameter: shown by `vela show`, in generated docs, in the OpenAPI schema and in VelaUX.", check: required("usage")},
	{Name: "short", Doc: "A one-letter flag for the parameter on the vela CLI.", check: func(v *string) string {
		if v == nil || len([]rune(*v)) != 1 {
			return "takes one character"
		}
		return ""
	}},
	{Name: "alias", Doc: "Another name for the parameter on the vela CLI.", check: required("alias")},
	{Name: "ignore", Bare: true, Doc: "Hides the parameter from the vela CLI and generated docs.", check: bare},
	{Name: "immutable", Bare: true, Doc: "Once set on an Application, the parameter cannot be changed or removed; on a struct, nothing under it can. The annotation app.oam.dev/force-param-mutations: \"true\" overrides it.", check: bare},
	{Name: "ui:type", Doc: "The VelaUX widget for the field, overriding the one its type implies.", check: required("ui:type")},
	{Name: "ui:format", Values: []string{"table"}, Doc: "Lays a list of structs out as a table.", check: oneOf("ui:format", "table")},
	{Name: "ui:rowKey", Doc: "The field that identifies a row of a table.", check: required("ui:rowKey")},
	{Name: "ui:itemLabel", Doc: "The field that labels each item of a list.", check: required("ui:itemLabel")},
	{Name: "ui:label", Doc: "The field's label in VelaUX.", check: required("ui:label")},
	{Name: "ui:placeholder", Doc: "Placeholder text for the field in VelaUX.", check: required("ui:placeholder")},
	{Name: "ui:error", Doc: "The message VelaUX shows when the field is invalid.", check: required("ui:error")},
	{Name: "ui:section", Doc: "The section of the VelaUX form the field is shown in.", check: required("ui:section")},
	{Name: "ui:optionsFrom", Values: []string{"clusters", "configs:", "envs"}, Doc: "Offers values from configs of a template (configs:<template>), the clusters, or the environments.", check: func(v *string) string {
		if v != nil && (*v == "clusters" || *v == "envs" || (strings.HasPrefix(*v, "configs:") && len(*v) > len("configs:"))) {
			return ""
		}
		return "takes configs:<template>, clusters or envs"
	}},
	{Name: "ui:expression", Values: []string{"never"}, Doc: "never: VelaUX does not offer an expression for the field.", check: oneOf("ui:expression", "never")},
	{Name: "ui:suggest", Doc: "Values VelaUX suggests, separated by commas.", check: required("ui:suggest")},
	{Name: "ui:colSpan", Doc: "How many of the form's 24 columns the field spans.", check: wholeNumber},
	{Name: "ui:order", Doc: "Where the field comes in the form; lower first.", check: wholeNumber},
	{Name: "ui:advanced", Bare: true, Values: []string{"true"}, Doc: "Shows the field under the form's advanced options.", check: bareOrTrue},
	{Name: "ui:hidden", Bare: true, Values: []string{"true"}, Doc: "Hides the field from the VelaUX form.", check: bareOrTrue},
}

// patchStrategies are the values of +patchStrategy kubevela/pkg's patcher acts on.
var patchStrategies = []string{"retainKeys", "replace", "jsonPatch", "jsonMergePatch"}

// patchMarkers steer how a trait's patch is applied.
var patchMarkers = []Marker{
	{Name: "patchKey", Doc: "Merges the list below by this field of its items, rather than by position.", check: func(v *string) string {
		if v == nil || *v == "" || strings.ContainsAny(*v, " \t") {
			return "takes a field name: +patchKey=name"
		}
		return ""
	}},
	{Name: "patchStrategy", Values: patchStrategies, Doc: "How the field below is patched: retainKeys or replace swap it wholesale; on patch itself, jsonPatch or jsonMergePatch read the whole patch as RFC 6902 or RFC 7396.", check: func(v *string) string {
		if v == nil {
			return "takes a value: +patchStrategy=" + strings.Join(patchStrategies, "|")
		}
		return ""
	}},
}

// Markers lists every marker, for completion and hover.
func Markers() []Marker {
	return append(append([]Marker{}, parameterMarkers...), patchMarkers...)
}

var markerLine = regexp.MustCompile(`^//\s*\+([A-Za-z][A-Za-z0-9]*(?::[A-Za-z][A-Za-z0-9]*)?)(?:=(.*))?$`)

// checkMarkers reports markers that will do nothing: misspelt, in the header,
// or with a value their reader does not act on.
func (d *document) checkMarkers() []Diagnostic {
	var diags []Diagnostic
	var path []string
	var places []markerPlace
	ast.Walk(d.file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			path = append(path, labelName(x.Label))
			places = append(places, placeOf(path))
		case *ast.CommentGroup:
			place := placeOther
			if len(places) > 0 {
				place = places[len(places)-1]
			}
			for _, c := range x.List {
				if diag, ok := d.checkMarker(c, place); ok {
					diags = append(diags, diag)
				}
			}
		}
		return true
	}, func(n ast.Node) {
		if _, ok := n.(*ast.Field); ok {
			path, places = path[:len(path)-1], places[:len(places)-1]
		}
	})
	return diags
}

// placeOf is the place of the field at path.
func placeOf(path []string) markerPlace {
	switch {
	case len(path) == 0:
		return placeOther
	case path[0] != templateLabel:
		return placeHeader
	case len(path) >= 2 && (path[1] == "patch" || path[1] == "patchOutputs"):
		if len(path) == 2 {
			return placePatchRoot
		}
		return placePatch
	}
	return placeOther
}

func (d *document) checkMarker(c *ast.Comment, place markerPlace) (Diagnostic, bool) {
	m := markerLine.FindStringSubmatch(strings.TrimSpace(c.Text))
	if m == nil {
		return Diagnostic{}, false
	}
	name := m[1]
	var value *string
	if strings.Contains(strings.TrimSpace(c.Text), "+"+name+"=") {
		v := strings.TrimSpace(m[2])
		value = &v
	}
	at := func(msg string) (Diagnostic, bool) {
		start := c.Pos()
		col := start.Column() + strings.Index(c.Text, "+")
		pos := Position{Line: start.Line(), Column: col}
		return Diagnostic{
			Range:    Range{Start: pos, End: Position{Line: pos.Line, Column: col + 1 + len(name)}},
			Severity: SeverityWarning,
			Message:  msg,
		}, true
	}
	marker := findMarker(Markers(), name)
	switch {
	case marker == nil:
		// Only a near miss is reported: other +words are not markers.
		if suggestion := closestMarker(name); suggestion != "" {
			return at("unknown marker +" + name + ": did you mean +" + suggestion + "?")
		}
	case place == placeHeader:
		return at("markers have no effect in the definition's header")
	case name == "patchStrategy" && value != nil:
		return d.checkPatchStrategy(*value, place, at)
	default:
		if why := marker.check(value); why != "" {
			return at("+" + name + " " + why)
		}
	}
	return Diagnostic{}, false
}

func (d *document) checkPatchStrategy(value string, place markerPlace, at func(string) (Diagnostic, bool)) (Diagnostic, bool) {
	known := false
	for _, s := range patchStrategies {
		known = known || s == value
	}
	switch {
	case !known:
		return at(fmt.Sprintf("+patchStrategy=%s is not a strategy: %s, so it has no effect", value, orList(patchStrategies)))
	case (value == "jsonPatch" || value == "jsonMergePatch") && place == placePatch:
		return at(fmt.Sprintf("+patchStrategy=%s applies to the whole patch: put it above patch", value))
	}
	return Diagnostic{}, false
}

func findMarker(markers []Marker, name string) *Marker {
	for i := range markers {
		if markers[i].Name == name {
			return &markers[i]
		}
	}
	return nil
}

// closestMarker is the marker a misspelt name most likely meant, or "".
func closestMarker(name string) string {
	best, bestDist := "", 3
	for _, m := range Markers() {
		if dist := editDistance(strings.ToLower(name), strings.ToLower(m.Name)); dist < bestDist {
			best, bestDist = m.Name, dist
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
