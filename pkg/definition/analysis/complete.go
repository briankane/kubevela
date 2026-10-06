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
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
)

// Completion is one candidate to complete the text before the cursor with.
type Completion struct {
	// Label is what the candidate is shown as.
	Label string
	// Insert replaces the last Replace bytes before the cursor.
	Insert  string
	Replace int
	Doc     string
	// Detail is the candidate's type, when it has one.
	Detail string
	// Snippet, when set, is inserted in place of Insert, with ${n} tab stops
	// and \$ for a literal dollar.
	Snippet string
}

var (
	markerNameTyped  = regexp.MustCompile(`^\s*//\s*\+((?:[A-Za-z][A-Za-z0-9]*(?::[A-Za-z0-9-]*)?)?)$`)
	markerValueTyped = regexp.MustCompile(`^\s*//\s*\+([A-Za-z][A-Za-z0-9]*(?::[A-Za-z][A-Za-z0-9]*)?)=(\S*)$`)
)

// CompleteMarker completes a marker in a comment, given its line up to the
// cursor: a marker's name after `// +`, or one of the values it takes after
// `=`.
func CompleteMarker(before string) []Completion {
	if m := markerNameTyped.FindStringSubmatch(before); m != nil {
		typed := m[1]
		var out []Completion
		for _, mk := range Markers() {
			if !strings.HasPrefix(strings.ToLower(mk.Name), strings.ToLower(typed)) {
				continue
			}
			insert := mk.Name
			if !mk.Bare {
				insert += "="
			}
			out = append(out, Completion{Label: "+" + mk.Name, Insert: insert, Replace: len(typed), Doc: mk.Doc})
		}
		return out
	}
	if m := markerValueTyped.FindStringSubmatch(before); m != nil {
		mk := findMarker(Markers(), m[1])
		if mk == nil {
			return nil
		}
		values := append([]string{}, mk.Values...)
		sort.Strings(values)
		var out []Completion
		for _, v := range values {
			if strings.HasPrefix(v, m[2]) {
				out = append(out, Completion{Label: v, Insert: v, Replace: len(m[2]), Doc: mk.Doc})
			}
		}
		return out
	}
	return nil
}

var (
	contextTyped = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.$#])context((?:\.[A-Za-z_][A-Za-z0-9_]*)*)\.([A-Za-z0-9_]*)$`)
	// headerType reads a definition's type from text that may not parse.
	headerType = regexp.MustCompile(`\btype:\s*"(component|trait|policy|workflow-step|source|workload)"`)
)

var applicationScope = regexp.MustCompile(`\bscope:\s*"Application"`)

// kindFromText is a definition's template kind from text that may not
// parse: its type, or an Application-scoped policy.
func kindFromText(doc, defType string) string {
	if defType == policyType && applicationScope.MatchString(doc) {
		return applicationPolicy
	}
	return defType
}

// CompleteContext completes a read of context, given the whole document,
// whose header names the definition type, and its line up to the cursor: the
// fields that type's template can read after `context.`, or a struct field's
// fields after `context.a.`.
func CompleteContext(doc, before string) []Completion {
	return CompleteContextWith(doc, before, nil)
}

// CompleteContextWith is CompleteContext, with what global policies publish
// offered under context.custom.
func CompleteContextWith(doc, before string, published []Published) []Completion {
	m := contextTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	t := headerType.FindStringSubmatch(doc)
	if t == nil {
		return nil
	}
	fields := ContextFields(t[1])
	if fields == nil {
		return nil
	}
	path, typed := strings.Split(strings.TrimPrefix(m[1], "."), "."), m[2]
	if m[1] == "" {
		path = nil
	}
	var out []Completion
	if len(path) == 0 {
		for _, f := range fields {
			if strings.HasPrefix(f.Name, typed) {
				out = append(out, Completion{Label: f.Name, Insert: f.Name, Replace: len(typed), Doc: f.Doc, Detail: f.Type})
			}
		}
		return out
	}
	if path[0] == "custom" {
		fields = customFields(published)
		if len(path) == 1 {
			var out []Completion
			for _, f := range fields {
				if strings.HasPrefix(f.Name, typed) {
					out = append(out, Completion{Label: f.Name, Insert: f.Name, Replace: len(typed), Doc: f.Doc, Detail: f.Type})
				}
			}
			return out
		}
		path = path[1:]
	}
	var root *ContextField
	for i := range fields {
		if fields[i].Name == path[0] {
			root = &fields[i]
		}
	}
	if root == nil {
		return nil
	}
	v := cuecontext.New().CompileString("x: " + root.Type).LookupPath(cue.ParsePath("x"))
	for _, p := range path[1:] {
		v = v.LookupPath(cue.MakePath(cue.Str(p)))
	}
	it, err := v.Fields(cue.Optional(true))
	if err != nil {
		return nil
	}
	for it.Next() {
		name := it.Selector().Unquoted()
		if strings.HasPrefix(name, typed) {
			out = append(out, Completion{Label: name, Insert: name, Replace: len(typed), Detail: typeOf(it.Value())})
		}
	}
	return out
}

func typeOf(v cue.Value) string {
	b, err := format.Node(v.Syntax())
	if err != nil {
		return ""
	}
	return string(b)
}

var (
	memberTyped = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.$#])([A-Za-z_][A-Za-z0-9_]*)\.(#?[A-Za-z0-9_]*)$`)
	importSpec  = regexp.MustCompile(`(?m)^\s*(?:import\s+)?(?:([A-Za-z_][A-Za-z0-9_]*)\s+)?"([A-Za-z0-9_.\-]+/[^"]+)"`)
	importTyped = regexp.MustCompile(`^\s*(?:import\s+)?(?:[A-Za-z_][A-Za-z0-9_]*\s+)?"([A-Za-z0-9/]*)$`)
)

// CompletePackageMember completes a member of an imported vela/* package,
// given the whole document and its line up to the cursor: the package's
// definitions after `kube.`, each with what its $params take.
func CompletePackageMember(doc, before string) []Completion {
	return CompletePackageMemberWith(doc, before, nil)
}

// CompletePackageMemberWith is CompletePackageMember, with the workspace's
// custom provider packages importable too.
func CompletePackageMemberWith(doc, before string, ext *Externals) []Completion {
	m := memberTyped.FindStringSubmatch(before)
	if m == nil {
		return nil
	}
	name, typed := m[1], m[2]
	var path string
	for _, spec := range importSpec.FindAllStringSubmatch(doc, -1) {
		alias := spec[1]
		if alias == "" {
			alias = spec[2][strings.LastIndex(spec[2], "/")+1:]
		}
		if alias == name {
			path = spec[2]
		}
	}
	t := headerType.FindStringSubmatch(doc)
	if path == "" || t == nil {
		return nil
	}
	pkgs := packages{builtin: packagesFor(kindFromText(doc, t[1])), ext: ext}
	pkg, ok := pkgs.value(path)
	if !ok {
		return nil
	}
	it, err := pkg.Fields(cue.Definitions(true))
	if err != nil {
		return nil
	}
	var out []Completion
	for it.Next() {
		label := it.Selector().String()
		if !strings.HasPrefix(label, "#") || !strings.HasPrefix(label, typed) {
			continue
		}
		member := it.Value()
		if documented, ok := pkgs.documented(path); ok {
			if d := documented.LookupPath(cue.MakePath(cue.Def(label))); d.Exists() {
				member = d
			}
		}
		detail, doc := describeMember(member)
		out = append(out, Completion{Label: label, Insert: label, Replace: len(typed), Detail: detail, Doc: doc, Snippet: callSnippet(label, it.Value())})
	}
	return out
}

// describeMember summarises a provider function: the $params it takes, and
// each one's +usage.
func describeMember(v cue.Value) (detail, doc string) {
	params := v.LookupPath(cue.MakePath(cue.Str("$params")))
	it, err := params.Fields(cue.Optional(true))
	if err != nil {
		return "", ""
	}
	var names, lines []string
	for it.Next() {
		name := it.Selector().Unquoted()
		if it.IsOptional() {
			name += "?"
		}
		names = append(names, name)
		line := "- `" + name + "`"
		if usage := usageOf(it.Value()); usage != "" {
			line += ": " + usage
		}
		lines = append(lines, line)
	}
	if returns := usageOf(v.LookupPath(cue.MakePath(cue.Str("$returns")))); returns != "" {
		lines = append(lines, "", "Returns: "+returns)
	}
	return "$params: " + strings.Join(names, ", "), strings.Join(lines, "\n")
}

// CompleteImport completes the path of an import, given the whole document
// and its text up to the cursor: the vela/* packages the definition type can
// import, on an import line or inside an import block.
func CompleteImport(doc, before string) []Completion {
	return CompleteImportWith(doc, before, nil)
}

// CompleteImportWith is CompleteImport, offering the workspace's custom
// provider packages too.
func CompleteImportWith(doc, before string, ext *Externals) []Completion {
	lines := strings.Split(before, "\n")
	last := lines[len(lines)-1]
	m := importTyped.FindStringSubmatch(last)
	if m == nil {
		return nil
	}
	if !strings.Contains(last, "import") {
		rest := strings.Join(lines[:len(lines)-1], "\n")
		open := strings.LastIndex(rest, "import (")
		if open < 0 || strings.Contains(rest[open:], ")") {
			return nil
		}
	}
	t := headerType.FindStringSubmatch(doc)
	if t == nil {
		return nil
	}
	typed := m[1]
	var out []Completion
	for _, p := range (packages{builtin: packagesFor(kindFromText(doc, t[1])), ext: ext}).list() {
		path := p.GetPath()
		if strings.HasPrefix(path, typed) {
			out = append(out, Completion{Label: path, Insert: path, Replace: len(typed), Doc: "The " + p.GetName() + " package."})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// customFields are the fields global policies publish, each named once.
func customFields(published []Published) []ContextField {
	seen := map[string]bool{}
	var out []ContextField
	for _, p := range published {
		for _, f := range p.Fields {
			if !seen[f.Name] {
				seen[f.Name] = true
				out = append(out, f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// callSnippet is a call of a provider function with its required $params
// to fill in: those neither optional nor defaulted, a struct of required
// fields spelled out, anything else one tab stop.
func callSnippet(label string, fn cue.Value) string {
	n := 0
	body := requiredParams(fn.LookupPath(cue.MakePath(cue.Str("$params"))), "\t\t", &n)
	if body == "" {
		body = "\t\t$0\n"
	}
	return label + " & {\n\t\\$params: {\n" + body + "\t}\n}"
}

// requiredParams writes the required fields of a struct, each on a line at
// indent, numbering the tab stops from n.
func requiredParams(v cue.Value, indent string, n *int) string {
	it, err := v.Fields()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for it.Next() {
		f := it.Value()
		if !isRequired(f) {
			continue
		}
		name := snippetEscape(it.Selector().Unquoted())
		if f.IncompleteKind() == cue.StructKind && hasDeclaredFields(f) {
			// A struct of declared fields is required only for those of its
			// fields that are; one with none open is a value to give whole.
			if nested := requiredParams(f, indent+"\t", n); nested != "" {
				b.WriteString(indent + name + ": {\n" + nested + indent + "}\n")
			}
			continue
		}
		*n++
		b.WriteString(fmt.Sprintf("%s%s: ${%d}\n", indent, name, *n))
	}
	return b.String()
}

// snippetEscape escapes what a snippet would read as syntax.
func snippetEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "$", "\\$", "}", "\\}").Replace(s)
}

// hasDeclaredFields reports whether a struct declares any field, optional
// ones included.
func hasDeclaredFields(v cue.Value) bool {
	it, err := v.Fields(cue.Optional(true))
	return err == nil && it.Next()
}
