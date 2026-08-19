/*
Copyright 2021 The KubeVela Authors.

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

package definition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	cueformat "cuelang.org/go/cue/format"
	cueparser "cuelang.org/go/cue/parser"
	upstreamcuex "github.com/kubevela/pkg/cue/cuex"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/process"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	apitypes "github.com/oam-dev/kubevela/apis/types"
	velacuex "github.com/oam-dev/kubevela/pkg/cue/cuex"
	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/definition/cachekey"
	"github.com/oam-dev/kubevela/pkg/definition/celexpr"
	"github.com/oam-dev/kubevela/pkg/definition/propexpr"
)

type sourceCachePolicy struct {
	Key string
	// KeyInputs names the values folded into the identity hash, as generated
	// alongside the key. Recorded rather than re-derived, so inference stays a
	// build-time concern.
	KeyInputs      []string
	TTL            time.Duration
	OnStaleFailure string
}

// GetWorkloadTemplateKey returns the context key for storing workload templates

// resolveSourceExpressions substitutes $(...) expressions in a properties blob.
//
// surface names the call site, which decides both what a source may read from
// context and - once the compatibility check lands - whether it may be consumed
// here at all.
func resolveSourceExpressions(ctx process.Context, params interface{}, surface string) (interface{}, error) {
	if params == nil {
		return nil, nil
	}
	bt, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var normalized interface{}
	if err := json.Unmarshal(bt, &normalized); err != nil {
		return nil, err
	}

	// The Application render is one caller of the engine, not the owner of the
	// machinery. Everything specific to it - reading the pushed inputs, flattening
	// the render context, pushing the statuses back - lives here and nowhere else.
	in := sourceInputsFromContext(ctx)
	engine, err := NewSourceEngine(SourceEngineOptions{
		Surface:   surface,
		Context:   contextValuesFor(ctx),
		Bindings:  in.Bindings,
		Types:     in.Types,
		Templates: in.Templates,
		Sensitive: in.Sensitive,
		Store:     in.Store,
	})
	if err != nil {
		return nil, err
	}
	res, err := engine.Resolve(ctx.GetCtx(), normalized)
	if len(res.Statuses) > 0 {
		ctx.PushData(SourceResolutionStatusKey, res.Statuses)
	}
	if err != nil {
		return nil, err
	}
	return res.Properties, nil
}

// resolveSourceNode walks a properties blob, carrying the path it is at so a
// recorded read can say which property received the value. Without it status can
// report what was read but not where it went, which is the half that matters
// once a property is assembled from more than one source.

// resolveSourceNode walks a properties blob, carrying the path it is at so a
// recorded read can say which property received the value. Without it status can
// report what was read but not where it went, which is the half that matters
// once a property is assembled from more than one source.
func resolveSourceNode(node interface{}, resolver *sourceResolver, path string) (interface{}, error) {
	switch val := node.(type) {
	case map[string]interface{}:
		for k, child := range val {
			resolved, err := resolveSourceNode(child, resolver, joinPropertyPath(path, k))
			if err != nil {
				return nil, err
			}
			val[k] = resolved
		}
		return val, nil
	case []interface{}:
		for i, child := range val {
			resolved, err := resolveSourceNode(child, resolver, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			val[i] = resolved
		}
		return val, nil
	case string:
		return evaluateSourceExpression(val, resolver, path)
	default:
		return node, nil
	}
}

func joinPropertyPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// evaluateSourceExpression substitutes $(...) expressions in a property value.
//
// A value
// with no delimiter comes back byte-identical, so nothing that works today
// changes. What it adds is the ability to combine a resolved value with anything
// else, which the directive cannot do - it yields a whole value or nothing.
//
// Resolving here rather than at admission matters for status: reading a source
// through an expression must drive the same resolution and the same consumed-value
// recording a directive would have done, or a binding used only by an expression would
// show as unresolved.

// evaluateSourceExpression substitutes $(...) expressions in a property value.
//
// A value
// with no delimiter comes back byte-identical, so nothing that works today
// changes. What it adds is the ability to combine a resolved value with anything
// else, which the directive cannot do - it yields a whole value or nothing.
//
// Resolving here rather than at admission matters for status: reading a source
// through an expression must drive the same resolution and the same consumed-value
// recording a directive would have done, or a binding used only by an expression would
// show as unresolved.
func evaluateSourceExpression(raw string, resolver *sourceResolver, property string) (interface{}, error) {
	parsed, err := propexpr.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !parsed.HasExpr() {
		return raw, nil
	}

	resolved := map[string]map[string]interface{}{}
	for _, fragment := range parsed.Fragments {
		if !fragment.IsExpr() {
			continue
		}
		refs, rerr := expressionReferences(fragment.Expr)
		if rerr != nil {
			return nil, rerr
		}
		for _, ref := range refs {
			if !ref.IsSource() {
				continue
			}
			name := ref.Path[0]
			values, verr := resolver.resolve(name)
			if verr != nil {
				return nil, verr
			}
			resolved[name] = values

			// Record what the expression read, so status reports it exactly as a
			// status reports it - including +sensitive redaction, which
			// matches on the recorded path.
			path := strings.Join(ref.Path[1:], ".")
			if value, ok := lookupMapPath(values, path); ok {
				resolver.recordConsumedValue(name, resolver.sourceTypes[name], path, value, property)
			}
		}
	}

	return celEvalProperty(raw, resolved, resolver.expressionContext())
}

// expressionReferences extracts the reads an expression makes, through whichever
// engine is selected. Both must agree, or dependency ordering and +sensitive
// redaction would differ between them.

// expressionReferences extracts the reads an expression makes, through whichever
// engine is selected. Both must agree, or dependency ordering and +sensitive
// redaction would differ between them.
func expressionReferences(expr string) ([]propexpr.Reference, error) {
	env, err := celexpr.DynEnv()
	if err != nil {
		return nil, err
	}
	celRefs, err := celexpr.References(env, expr)
	if err != nil {
		return nil, err
	}
	out := make([]propexpr.Reference, 0, len(celRefs))
	for _, r := range celRefs {
		out = append(out, propexpr.Reference{
			Root: r.Root, Path: r.Path, Defaulted: r.Guarded,
		})
	}
	return out, nil
}

// celEvalProperty evaluates a whole property value with CEL, interpolation
// included. The $( ) splitting is shared, so only the contents differ.

// celEvalProperty evaluates a whole property value with CEL, interpolation
// included. The $( ) splitting is shared, so only the contents differ.
func celEvalProperty(raw string, resolved map[string]map[string]interface{},
	ctx map[string]interface{}) (interface{}, error) {
	env, err := celexpr.DynEnv()
	if err != nil {
		return nil, err
	}
	in := map[string]interface{}{"context": ctx}
	sources := map[string]interface{}{}
	for name, values := range resolved {
		sources[name] = values
	}
	in["source"] = sources
	return celexpr.EvalProperty(env, raw, in)
}

// expressionContext pulls the fields this surface declares readable out of
// the render's process context.

// expressionContext pulls the fields this surface declares readable out of
// the render's process context.
func (r *sourceResolver) expressionContext() map[string]interface{} {
	out := map[string]interface{}{}
	for _, field := range propexpr.ContextFor(r.surface).ReadableFields() {
		if v := r.ctxValues[field]; v != nil {
			out[field] = v
		}
	}
	return out
}

func lookupMapPath(data map[string]interface{}, path string) (interface{}, bool) {
	cur := interface{}(data)
	for _, p := range strings.Split(path, ".") {
		// A segment is an index when what it is being applied to is a list. The
		// reference carries indices as decimal text, and only the value decides
		// how to read them - the same rule the schema walk uses.
		if list, ok := cur.([]interface{}); ok {
			index, err := strconv.Atoi(p)
			if err != nil || index < 0 || index >= len(list) {
				return nil, false
			}
			cur = list[index]
			continue
		}
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		next, ok := m[p]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

type sourceResolver struct {
	// goCtx carries deadlines and cancellation into the fetches a template
	// performs.
	goCtx context.Context
	// ctxValues is the render context an expression and a template may read,
	// already narrowed to what the rules allow. A map rather than a
	// process.Context because reading field values is all either ever did, and
	// requiring the render's own context type would put this feature out of reach
	// of anything that is not an Application render.
	ctxValues map[string]interface{}
	// statuses accumulates what resolved, for the caller to report. Returned
	// rather than pushed back onto a context, so a caller that has no context to
	// push onto still gets it.
	statuses map[string]SourceResolutionStatus
	// readerKind and readerName name the thing currently doing the reading when it
	// is not the surface being rendered. Set while a chained source resolves its
	// own properties, empty otherwise.
	readerKind string
	readerName string
	// surface is the call site this resolver is working on behalf of. It decides
	// what a source may read from context: a chained source resolves inside
	// whichever render triggered the outer binding, so it inherits this too.
	surface         string
	sourceProps     map[string]map[string]interface{}
	sourceTypes     map[string]string
	sourceTemplates map[string]string
	sourceSchemas   map[string]string
	sensitivePaths  map[string][]string
	cacheStore      velaprocess.SourceCacheStore
	compiler        SourceCompiler
	resolved        map[string]map[string]interface{}
	resolving       map[string]bool
}

// SourceResolutionStatus captures source runtime resolution result.
// SourceRead is one value taken from a source: what was read, where it went,
// and who read it.

// SourceResolutionStatus captures source runtime resolution result.
// SourceRead is one value taken from a source: what was read, where it went,
// and who read it.
type SourceRead struct {
	// SourceAttr is the attribute of the source that was read, e.g. "data.image".
	SourceAttr string
	// Property is the consumer's property it landed in, e.g. "image" or
	// "env[0].value". Empty when the read happened somewhere without a property
	// path, which today means a source resolving its own properties.
	Property string
	// ReaderKind and ReaderName name the reader when it is not the surface being
	// rendered - a source whose own properties read an earlier source. Empty
	// means the enclosing component, trait or step.
	ReaderKind string
	ReaderName string
	Value      interface{}
}

type SourceResolutionStatus struct {
	Name           string
	Type           string
	Phase          string
	Message        string
	Config         string
	ExpiresAt      string
	ResolvedFields map[string]interface{}
	// ConsumedFields is field -> value, and is what the auto-update hash is
	// computed over. Deliberately left as a map: json.Marshal sorts map keys, so
	// the hash is stable, and moving it to an ordered list would risk every
	// existing workload's stamped hash changing on upgrade and re-dispatching.
	ConsumedFields map[string]interface{}
	// Reads is the same information with the destination attached: which property
	// of the consumer each value landed in, and which reader took it. Reporting
	// only, never hashed.
	Reads          []SourceRead
	SensitivePaths []string
}

// SourceCompiler evaluates a source's CUE template. Satisfied by
// *cuex.Compiler.
//
// An interface rather than the package singleton so a caller can supply its own
// provider set, and so a test can compile without reaching for global state. The
// resolver needs both methods: the template is evaluated with providers, and the
// storage block deliberately without them.

// SourceCompiler evaluates a source's CUE template. Satisfied by
// *cuex.Compiler.
//
// An interface rather than the package singleton so a caller can supply its own
// provider set, and so a test can compile without reaching for global state. The
// resolver needs both methods: the template is evaluated with providers, and the
// storage block deliberately without them.
type SourceCompiler interface {
	CompileString(ctx context.Context, src string) (cue.Value, error)
	CompileStringWithOptions(ctx context.Context, src string, opts ...upstreamcuex.CompileOption) (cue.Value, error)
}

// sourceInputs is everything a resolution needs that is not the properties being
// resolved: which bindings exist, what definition backs each, and where values
// are cached.
//
// Explicit rather than pulled from the render context. The Application controller
// pushes these onto process.Context for its own reasons, but a resolver should
// not have to know that protocol to be usable - and a second caller has no
// process.Context to push onto.

// sourceInputs is everything a resolution needs that is not the properties being
// resolved: which bindings exist, what definition backs each, and where values
// are cached.
//
// Explicit rather than pulled from the render context. The Application controller
// pushes these onto process.Context for its own reasons, but a resolver should
// not have to know that protocol to be usable - and a second caller has no
// process.Context to push onto.
type sourceInputs struct {
	// Bindings is spec.sources[].name -> that binding's own properties.
	Bindings map[string]map[string]interface{}
	// Types is binding name -> SourceDefinition type, carrying the pinned
	// revision where one was requested.
	Types map[string]string
	// Templates is definition type -> its CUE template.
	Templates map[string]string
	// Sensitive is definition type -> the paths its schema marks +sensitive.
	Sensitive map[string][]string
	// Store persists resolved values. Nil disables caching, which resolves
	// correctly and simply re-fetches.
	Store velaprocess.SourceCacheStore
	// Compiler evaluates source templates. Nil takes the workload compiler, which
	// is what the Application render has always used.
	Compiler SourceCompiler
}

// contextValuesFor flattens the render context into the field values a source may
// read, which is every field the cache-key rules allow. Narrowing to a surface
// happens later, when the source's own context block is rendered.

// contextValuesFor flattens the render context into the field values a source may
// read, which is every field the cache-key rules allow. Narrowing to a surface
// happens later, when the source's own context block is rendered.
func contextValuesFor(ctx process.Context) map[string]interface{} {
	rules, err := cachekey.LoadRules()
	if err != nil {
		klog.Warningf("loading cache key rules for source context: %v", err)
		return map[string]interface{}{}
	}
	out := map[string]interface{}{}
	for _, field := range rules.Fields() {
		if v := ctx.GetData(field); v != nil {
			out[field] = v
		}
	}
	// Readable context is a superset of keyed context: a surface may offer fields
	// an expression can read but a key may not be built from.
	for _, surface := range propexpr.SurfaceNames() {
		for _, field := range propexpr.ContextFor(surface).ReadableFields() {
			if _, have := out[field]; have {
				continue
			}
			if v := ctx.GetData(field); v != nil {
				out[field] = v
			}
		}
	}
	return out
}

// sourceInputsFromContext reads what the Application controller pushed. It is the
// bridge from the render context's protocol to explicit inputs, and the only
// place that protocol is understood.

// sourceInputsFromContext reads what the Application controller pushed. It is the
// bridge from the render context's protocol to explicit inputs, and the only
// place that protocol is understood.
func sourceInputsFromContext(ctx process.Context) sourceInputs {
	in := sourceInputs{
		Bindings:  map[string]map[string]interface{}{},
		Types:     map[string]string{},
		Templates: map[string]string{},
		Sensitive: map[string][]string{},
	}
	if v, ok := ctx.GetData(velaprocess.ContextAppSources).(map[string]map[string]interface{}); ok && v != nil {
		in.Bindings = v
	}
	if v, ok := ctx.GetData(velaprocess.ContextAppSourceTypes).(map[string]string); ok && v != nil {
		in.Types = v
	}
	if v, ok := ctx.GetData(velaprocess.ContextAppSourceTemplates).(map[string]string); ok && v != nil {
		in.Templates = v
	}
	if v, ok := ctx.GetData(velaprocess.ContextAppSourceSensitivePaths).(map[string][]string); ok && v != nil {
		in.Sensitive = v
	}
	if v, ok := ctx.GetData(velaprocess.ContextAppSourceCacheStore).(velaprocess.SourceCacheStore); ok && v != nil {
		in.Store = v
	}
	return in
}

// newSourceResolver builds a resolver for one render.
//
// ctx is still required, for three things that are genuinely the render's: the
// context values an expression may read, the Go context for I/O, and recording
// what resolved so status can report it.

// newSourceResolver builds a resolver for one render.
//
// ctx is still required, for three things that are genuinely the render's: the
// context values an expression may read, the Go context for I/O, and recording
// what resolved so status can report it.
func newSourceResolver(goCtx context.Context, ctxValues map[string]interface{}, surface string, in sourceInputs) *sourceResolver {
	// Schemas are derived from the templates rather than supplied: they are a
	// projection of the definition, so accepting them separately would allow the
	// two to disagree.
	sourceSchemas := map[string]string{}
	for sourceType, sourceTemplate := range in.Templates {
		schemaExpr, err := extractSourceSchemaExpr(sourceTemplate)
		if err != nil {
			klog.Warningf("extract source schema failed for %s: %v", sourceType, err)
			continue
		}
		if schemaExpr != "" {
			sourceSchemas[sourceType] = schemaExpr
		}
	}
	compiler := in.Compiler
	if compiler == nil {
		compiler = velacuex.WorkloadCompiler.Get()
	}
	return &sourceResolver{
		surface:         surface,
		goCtx:           goCtx,
		ctxValues:       ctxValues,
		statuses:        map[string]SourceResolutionStatus{},
		compiler:        compiler,
		sourceProps:     in.Bindings,
		sourceTypes:     in.Types,
		sourceTemplates: in.Templates,
		sourceSchemas:   sourceSchemas,
		sensitivePaths:  in.Sensitive,
		cacheStore:      in.Store,
		resolved:        map[string]map[string]interface{}{},
		resolving:       map[string]bool{},
	}
}

// extractUserErrors reads the authored `errs:` field ([]string) from a compiled
// CUE value and returns its non-empty entries. A malformed `errs:` field is
// logged and treated as empty so error reporting never masks the real result.

func (r *sourceResolver) resolve(sourceName string) (map[string]interface{}, error) {
	if v, ok := r.resolved[sourceName]; ok {
		return v, nil
	}
	if r.resolving[sourceName] {
		err := fmt.Errorf("circular source dependency detected at %q", sourceName)
		r.setSourceStatus(sourceName, "", "Failed", err.Error(), "", "", nil)
		return nil, err
	}
	r.resolving[sourceName] = true
	defer delete(r.resolving, sourceName)

	sourceType, ok := r.sourceTypes[sourceName]
	if !ok || sourceType == "" {
		err := fmt.Errorf("source %q not found", sourceName)
		r.setSourceStatus(sourceName, "", "Failed", err.Error(), "", "", nil)
		return nil, err
	}
	sourceTemplate, ok := r.sourceTemplates[sourceType]
	if !ok || sourceTemplate == "" {
		err := fmt.Errorf("source definition %q for source %q is missing cue template", sourceType, sourceName)
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
		return nil, err
	}
	resolvedProps := map[string]interface{}{}
	paramFile := velaprocess.ParameterFieldName + ": {}"
	if props, ok := r.sourceProps[sourceName]; ok && props != nil {
		// A source's own properties may read an earlier source. Those reads belong
		// to this binding, not to the component whose render happened to trigger
		// the chain - without this the chain is invisible and the reads look like
		// the component made them directly.
		prevKind, prevName := r.readerKind, r.readerName
		r.readerKind, r.readerName = "source", sourceName
		resolvedPropsNode, err := resolveSourceNode(props, r, "")
		r.readerKind, r.readerName = prevKind, prevName
		if err != nil {
			r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
			return nil, errors.WithMessagef(err, "resolve source properties for %s", sourceName)
		}
		rp, ok := resolvedPropsNode.(map[string]interface{})
		if !ok {
			err := fmt.Errorf("resolved source properties for %s are invalid", sourceName)
			r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
			return nil, err
		}
		resolvedProps = rp
		raw, err := json.Marshal(rp)
		if err != nil {
			r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
			return nil, errors.WithMessagef(err, "marshal properties for source %s", sourceName)
		}
		paramFile = fmt.Sprintf("%s: %s", velaprocess.ParameterFieldName, string(raw))
	}
	cachePolicy, err := r.resolveCachePolicy(sourceName, sourceType, sourceTemplate, resolvedProps)
	if err != nil {
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
		return nil, err
	}
	// storage.key is the readable prefix; uniqueness comes from the hash below,
	// which covers the definition's template, the binding's properties, and
	// exactly the context values the template reads.
	identity := identityInputs{
		Template:   templateFingerprint(sourceTemplate),
		Properties: resolvedProps,
		Context:    identityContext(r.ctxValues, sourceName, cachePolicy.KeyInputs),
	}
	cachePolicy.Key, err = cacheIdentity(cachePolicy.Key, identity)
	if err != nil {
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), "", "", nil)
		return nil, err
	}
	cached, stale, found, cacheExpiresAt, err := r.readSourceCache(cachePolicy.Key, cachePolicy.TTL)
	if err != nil {
		klog.Warningf("read source cache failed for %s: %v", sourceName, err)
	} else if found {
		if !stale {
			r.resolved[sourceName] = cached
			r.setSourceStatus(sourceName, sourceType, "Resolved", "", cachePolicy.Key, formatExpiry(cacheExpiresAt), cached)
			return cached, nil
		}
	}
	// A source is compiled against the context the cache-key rules make readable,
	// not the component's - so it cannot depend on anything the key ignores.
	c, err := sourceContext(r.ctxValues, sourceName, r.surface)
	if err != nil {
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), cachePolicy.Key, "", nil)
		return nil, err
	}
	val, err := r.compiler.CompileString(r.goCtx, strings.Join([]string{
		renderTemplate(sourceTemplate), paramFile, c,
	}, "\n"))
	if err != nil {
		if found && stale && cachePolicy.OnStaleFailure == sourceCachePolicyUseStale {
			r.touchSourceCache(cachePolicy.Key)
			r.resolved[sourceName] = cached
			r.setSourceStatus(sourceName, sourceType, "Resolved", "refresh failed; serving stale cached value", cachePolicy.Key, formatExpiry(cacheExpiresAt), cached)
			return cached, nil
		}
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), cachePolicy.Key, "", nil)
		return nil, errors.WithMessagef(err, "compile source definition %s", sourceType)
	}
	if userErrs := extractUserErrors(val, "source definition", sourceType); len(userErrs) > 0 {
		errMsg := strings.Join(userErrs, "; ")
		if found && stale && cachePolicy.OnStaleFailure == sourceCachePolicyUseStale {
			r.touchSourceCache(cachePolicy.Key)
			r.resolved[sourceName] = cached
			r.setSourceStatus(sourceName, sourceType, "Resolved", "refresh reported errors; serving stale cached value", cachePolicy.Key, formatExpiry(cacheExpiresAt), cached)
			return cached, nil
		}
		r.setSourceStatus(sourceName, sourceType, "Failed", errMsg, cachePolicy.Key, "", nil)
		return nil, fmt.Errorf("source definition %s reported errors: %s", sourceType, errMsg)
	}
	output := map[string]interface{}{}
	if err := val.LookupPath(value.FieldPath(OutputFieldName)).Decode(&output); err != nil {
		if found && stale && cachePolicy.OnStaleFailure == sourceCachePolicyUseStale {
			r.touchSourceCache(cachePolicy.Key)
			r.resolved[sourceName] = cached
			r.setSourceStatus(sourceName, sourceType, "Resolved", "refresh failed; serving stale cached value", cachePolicy.Key, formatExpiry(cacheExpiresAt), cached)
			return cached, nil
		}
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), cachePolicy.Key, "", nil)
		return nil, errors.WithMessagef(err, "decode output for source definition %s", sourceType)
	}
	if err := r.validateResolvedOutput(sourceType, sourceTemplate, output); err != nil {
		if found && stale && cachePolicy.OnStaleFailure == sourceCachePolicyUseStale {
			r.touchSourceCache(cachePolicy.Key)
			r.resolved[sourceName] = cached
			r.setSourceStatus(sourceName, sourceType, "Resolved", "refresh failed; serving stale cached value", cachePolicy.Key, formatExpiry(cacheExpiresAt), cached)
			return cached, nil
		}
		r.setSourceStatus(sourceName, sourceType, "Failed", err.Error(), cachePolicy.Key, "", nil)
		return nil, errors.WithMessagef(err, "validate output against schema for source definition %s", sourceType)
	}
	r.resolved[sourceName] = output
	expiresAt := time.Now().Add(cachePolicy.TTL).Format(time.RFC3339)
	if err := r.writeSourceCache(cachePolicy.Key, sourceType, output, cachePolicy.TTL,
		cachePolicy.KeyInputs, identity); err != nil {
		klog.Warningf("write source cache failed for %s: %v", sourceName, err)
	}
	r.setSourceStatus(sourceName, sourceType, "Resolved", "", cachePolicy.Key, expiresAt, output)
	return output, nil
}

func (r *sourceResolver) resolveCachePolicy(sourceName, sourceType, sourceTemplate string, props map[string]interface{}) (sourceCachePolicy, error) {
	policy := sourceCachePolicy{
		TTL:            sourceCacheTTL,
		OnStaleFailure: sourceCachePolicyUseStale,
	}
	if sourceTemplate == "" {
		return policy, fmt.Errorf("source definition %q has no cue template", sourceType)
	}
	paramFile := velaprocess.ParameterFieldName + ": {}"
	if len(props) > 0 {
		if raw, err := json.Marshal(props); err == nil {
			paramFile = fmt.Sprintf("%s: %s", velaprocess.ParameterFieldName, string(raw))
		}
	}
	c, err := sourceContext(r.ctxValues, sourceName, r.surface)
	if err != nil {
		return policy, err
	}
	// storage: is pure interpolation over context and parameter values, so it is
	// resolved WITHOUT running provider functions. Resolving them here would
	// perform the very I/O the cache exists to avoid - on every reconcile, before
	// the cache is even consulted.
	val, err := r.compiler.CompileStringWithOptions(r.goCtx, strings.Join([]string{
		renderTemplate(sourceTemplate), paramFile, c,
	}, "\n"), upstreamcuex.DisableResolveProviderFunctions{})
	if err != nil {
		return policy, errors.WithMessagef(err, "evaluate storage block for source %q", sourceName)
	}
	// The generated block. It is written by `vela def` and re-derived at
	// admission, so a definition that reached the cluster always has one.
	internal := val.LookupPath(value.FieldPath(cachekey.InternalField))
	if !internal.Exists() {
		return policy, fmt.Errorf("source definition %q has no %s block; apply it with `vela def apply` "+
			"so the cache key is generated", sourceType, cachekey.InternalField)
	}

	cacheKey := ""
	if err := internal.LookupPath(value.FieldPath(cachekey.KeyField)).Decode(&cacheKey); err != nil {
		return policy, errors.WithMessagef(err, "resolve %s.%s for source %q",
			cachekey.InternalField, cachekey.KeyField, sourceName)
	}
	if err := cachekey.ValidateCacheKey(cacheKey); err != nil {
		return policy, errors.WithMessagef(err, "source %q", sourceName)
	}
	policy.Key = cacheKey

	var keyInputs []string
	if err := internal.LookupPath(value.FieldPath(cachekey.KeyInputsField)).Decode(&keyInputs); err == nil {
		policy.KeyInputs = keyInputs
	}

	// storage: is authored and entirely optional - a source with no caching
	// preferences declares nothing.
	storage := val.LookupPath(value.FieldPath("storage"))

	ttlRaw := ""
	if err := storage.LookupPath(value.FieldPath("storageTTL")).Decode(&ttlRaw); err == nil && ttlRaw != "" {
		ttl, err := time.ParseDuration(ttlRaw)
		if err != nil {
			return policy, fmt.Errorf("source %q has an invalid storageTTL %q: %w", sourceName, ttlRaw, err)
		}
		if ttl <= 0 {
			return policy, fmt.Errorf("source %q has a non-positive storageTTL %q", sourceName, ttlRaw)
		}
		policy.TTL = ttl
	}

	onStaleFailure := ""
	if err := storage.LookupPath(value.FieldPath("onStaleFailure")).Decode(&onStaleFailure); err == nil && onStaleFailure != "" {
		switch onStaleFailure {
		case sourceCachePolicyUseStale, sourceCachePolicyFail:
			policy.OnStaleFailure = onStaleFailure
		default:
			// Silently defaulting here would downgrade a definition that asked to
			// fail on stale data into one that serves it.
			return policy, fmt.Errorf("source %q has an unknown onStaleFailure %q: expected %q or %q",
				sourceName, onStaleFailure, sourceCachePolicyUseStale, sourceCachePolicyFail)
		}
	}
	return policy, nil
}

func (r *sourceResolver) validateResolvedOutput(sourceType, sourceTemplate string, output map[string]interface{}) error {
	schemaExpr := r.sourceSchemas[sourceType]
	if schemaExpr == "" {
		extracted, err := extractSourceSchemaExpr(sourceTemplate)
		if err != nil {
			return err
		}
		if extracted == "" {
			return nil
		}
		schemaExpr = extracted
		r.sourceSchemas[sourceType] = extracted
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	v := cuecontext.New().CompileString(fmt.Sprintf("schema: %s\noutput: close(schema) & %s", schemaExpr, string(raw)))
	if v.Err() != nil {
		return v.Err()
	}
	out := v.LookupPath(cue.ParsePath("output"))
	if !out.Exists() {
		return fmt.Errorf("source output missing")
	}
	return out.Validate(cue.Concrete(true))
}

func extractSourceSchemaExpr(template string) (string, error) {
	file, err := cueparser.ParseFile("-", template, cueparser.ParseComments)
	if err != nil {
		return "", err
	}
	for _, decl := range file.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		name, _, err := ast.LabelName(field.Label)
		if err != nil || name != "schema" {
			continue
		}
		bt, err := cueformat.Node(field.Value)
		if err != nil {
			return "", err
		}
		return string(bt), nil
	}
	return "", nil
}

func (r *sourceResolver) readSourceCache(cacheKey string, ttl time.Duration) (map[string]interface{}, bool, bool, time.Time, error) {
	if r.cacheStore == nil || cacheKey == "" {
		return nil, false, false, time.Time{}, nil
	}
	return r.cacheStore.Read(r.goCtx, cacheKey, ttl)
}

func (r *sourceResolver) writeSourceCache(cacheKey, sourceType string, data map[string]interface{},
	ttl time.Duration, keyInputs []string, identity identityInputs) error {
	if r.cacheStore == nil || cacheKey == "" {
		return nil
	}
	namespace, _ := r.ctxValues[velaprocess.ContextNamespace].(string)
	meta := velaprocess.SourceCacheWriteMeta{
		TTL:                ttl,
		SourceDefName:      sourceType,
		SourceDefNamespace: namespace,
		TemplateName:       sourceCacheTemplateName(sourceType, r.sourceSchemas[sourceType]),
		KeyInputs:          keyInputs,
		Context:            identity.Context,
		Properties:         identity.Properties,
		TemplateHash:       identity.Template,
	}
	return r.cacheStore.Write(r.goCtx, cacheKey, sourceType, data, meta)
}

// touchSourceCache advances the last-accessed marker for a stale entry that is
// being served, if the backing store supports it. Failures are non-fatal: a
// missed touch only risks the sweep collecting a still-used entry one cycle
// early, which the next render re-creates.

// touchSourceCache advances the last-accessed marker for a stale entry that is
// being served, if the backing store supports it. Failures are non-fatal: a
// missed touch only risks the sweep collecting a still-used entry one cycle
// early, which the next render re-creates.
func (r *sourceResolver) touchSourceCache(cacheKey string) {
	if r.cacheStore == nil || cacheKey == "" {
		return
	}
	toucher, ok := r.cacheStore.(velaprocess.SourceCacheToucher)
	if !ok {
		return
	}
	if err := toucher.Touch(r.goCtx, cacheKey); err != nil {
		klog.Warningf("touch source cache failed for %s: %v", cacheKey, err)
	}
}

// sourceCacheTemplateName reproduces the ConfigTemplate name the SourceDefinition
// controller derives from (sourceType, schema) so a cache entry can be stamped
// with the template it was rendered against without a client round-trip. It must
// stay in sync with buildSchemaTemplateName in the sourcedefinition controller.
// Returns "" when there is no schema (no template is generated in that case).

// sourceCacheTemplateName reproduces the ConfigTemplate name the SourceDefinition
// controller derives from (sourceType, schema) so a cache entry can be stamped
// with the template it was rendered against without a client round-trip. It must
// stay in sync with buildSchemaTemplateName in the sourcedefinition controller.
// Returns "" when there is no schema (no template is generated in that case).
func sourceCacheTemplateName(sourceType, schemaExpr string) string {
	if schemaExpr == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(schemaExpr))
	shortHash := hex.EncodeToString(sum[:])[:8]
	safeName := sanitizeSourceName(sourceType)
	if safeName == "" {
		safeName = "source"
	}
	const prefix = "source-"
	suffix := "-" + shortHash
	maxNameLen := 63 - len(prefix) - len(suffix)
	if maxNameLen < 1 {
		maxNameLen = 1
	}
	if len(safeName) > maxNameLen {
		safeName = strings.Trim(safeName[:maxNameLen], "-")
		if safeName == "" {
			safeName = "source"
		}
	}
	return prefix + safeName + suffix
}

func sanitizeSourceName(name string) string {
	s := strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// applySourceCacheMetadata stamps identity and lifetime metadata onto a source
// cache object so a context-free GC sweep can reason about it. It is strictly
// additive: it never overwrites the config.oam.dev/type label, which callers
// (e.g. the config-API store via ParseConfig) set to the ConfigTemplate name and
// which the config factory relies on for its change-template guard. The
// ttl/template/sourcedefinition markers are new.

// applySourceCacheMetadata stamps identity and lifetime metadata onto a source
// cache object so a context-free GC sweep can reason about it. It is strictly
// additive: it never overwrites the config.oam.dev/type label, which callers
// (e.g. the config-API store via ParseConfig) set to the ConfigTemplate name and
// which the config factory relies on for its change-template guard. The
// ttl/template/sourcedefinition markers are new.
func ApplySourceCacheMetadata(obj metav1.Object, sourceType string, meta velaprocess.SourceCacheWriteMeta) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[apitypes.LabelConfigCatalog] = apitypes.VelaCoreConfig
	// Preserve an existing type (the template name set by ParseConfig); only fall
	// back to the source type when nothing linked a template (the Secret-store
	// path, which has no ConfigTemplate).
	if labels[apitypes.LabelConfigType] == "" {
		labels[apitypes.LabelConfigType] = sourceType
	}
	if meta.SourceDefName != "" {
		labels[apitypes.LabelSourceDefinitionName] = meta.SourceDefName
	}
	if meta.SourceDefNamespace != "" {
		labels[apitypes.LabelSourceDefinitionNamespace] = meta.SourceDefNamespace
	}
	for k, v := range contextLabels(meta.Context) {
		labels[k] = v
	}
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if meta.TTL > 0 {
		annotations[sourceCacheTTLKey] = meta.TTL.String()
	}
	if meta.TemplateName != "" {
		annotations[sourceCacheTemplateKey] = meta.TemplateName
	}
	if meta.TemplateHash != "" {
		annotations[apitypes.AnnotationSourceTemplateHash] = meta.TemplateHash
	}
	if len(meta.KeyInputs) > 0 {
		if raw, err := json.Marshal(meta.KeyInputs); err == nil {
			annotations[apitypes.AnnotationSourceKeyInputs] = string(raw)
		}
	}
	if len(meta.Context) > 0 {
		if raw, err := json.Marshal(meta.Context); err == nil {
			annotations[apitypes.AnnotationSourceContext] = string(raw)
		}
	}
	if len(meta.Properties) > 0 {
		if raw, truncated, err := renderProperties(meta.Properties); err == nil {
			annotations[apitypes.AnnotationSourceProperties] = raw
			if truncated {
				// Say so explicitly, so a clipped value is never mistaken for the
				// real one when someone is comparing two entries.
				annotations[apitypes.AnnotationSourcePropertiesTruncated] = "true"
			}
		}
	}
	obj.SetAnnotations(annotations)
}

// maxAnnotationValueLen caps a single recorded value. Kubernetes budgets 256KB
// across all annotations on an object; these are diagnostic, so they take a
// small slice of that and leave the rest to whatever else annotates the entry.

// shouldTouchSourceCache throttles last-accessed updates: it returns true only
// when no marker exists yet or the existing one is older than half the entry's
// TTL, so a hot stale entry is not rewritten on every reconcile. The TTL is read
// from the entry's own annotation, defaulting to sourceCacheTTL.
func ShouldTouchSourceCache(annotations map[string]string, now time.Time) bool {
	if annotations == nil {
		return true
	}
	raw := annotations[sourceCacheAccessedKey]
	if raw == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	ttl := sourceCacheTTL
	if t := annotations[sourceCacheTTLKey]; t != "" {
		if parsed, perr := time.ParseDuration(t); perr == nil && parsed > 0 {
			ttl = parsed
		}
	}
	return now.Sub(last) >= ttl/2
}

func (r *sourceResolver) setSourceStatus(sourceName, sourceType, phase, message, config, expiresAt string, resolved map[string]interface{}) {
	statuses := r.statuses
	current := statuses[sourceName]
	consumed := current.ConsumedFields
	if consumed == nil {
		consumed = map[string]interface{}{}
	}
	statuses[sourceName] = SourceResolutionStatus{
		Name:           sourceName,
		Type:           sourceType,
		Phase:          phase,
		Message:        message,
		Config:         config,
		ExpiresAt:      expiresAt,
		ResolvedFields: resolved,
		ConsumedFields: consumed,
		SensitivePaths: append([]string{}, r.sensitivePaths[sourceType]...),
	}
}

func (r *sourceResolver) recordConsumedValue(sourceName, sourceType, path string, v interface{}, property string) {
	statuses := r.statuses
	st := statuses[sourceName]
	if st.Name == "" {
		st.Name = sourceName
	}
	if st.Type == "" {
		st.Type = sourceType
	}
	if st.ConsumedFields == nil {
		st.ConsumedFields = map[string]interface{}{}
	}
	st.ConsumedFields[path] = v
	st.Reads = append(st.Reads, SourceRead{
		SourceAttr: path,
		Property:   property,
		ReaderKind: r.readerKind,
		ReaderName: r.readerName,
		Value:      v,
	})
	if len(st.SensitivePaths) == 0 {
		st.SensitivePaths = append([]string{}, r.sensitivePaths[sourceType]...)
	}
	statuses[sourceName] = st
}

// formatExpiry renders a cache expiry, and renders nothing when there is not
// one. A source whose definition sets no storageTTL has a zero time, and
// formatting that put "0001-01-01T00:00:00Z" into status - a date, where the
// honest answer is silence.

// formatExpiry renders a cache expiry, and renders nothing when there is not
// one. A source whose definition sets no storageTTL has a zero time, and
// formatting that put "0001-01-01T00:00:00Z" into status - a date, where the
// honest answer is silence.
func formatExpiry(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
