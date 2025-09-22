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
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/oam-dev/kubevela/pkg/cue/definition/health"

	"github.com/kubevela/pkg/cue/cuex"

	"cuelang.org/go/cue"
	cueErrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/format"
	"github.com/kubevela/pkg/multicluster"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/model/sets"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/process"

	velaprocess "github.com/oam-dev/kubevela/pkg/cue/process"
	"github.com/oam-dev/kubevela/pkg/cue/task"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
)

const (
	// OutputFieldName is the name of the struct contains the CR data
	OutputFieldName = velaprocess.OutputFieldName
	// OutputsFieldName is the name of the struct contains the map[string]CR data
	OutputsFieldName = velaprocess.OutputsFieldName
	// PatchFieldName is the name of the struct contains the patch of CR data
	PatchFieldName = "patch"
	// PatchOutputsFieldName is the name of the struct contains the patch of outputs CR data
	PatchOutputsFieldName = "patchOutputs"
	// ErrsFieldName check if errors contained in the cue
	ErrsFieldName = "errs"
)

const (
	// AuxiliaryWorkload defines the extra workload obj from a workloadDefinition,
	// e.g. a workload composed by deployment and service, the service will be marked as AuxiliaryWorkload
	AuxiliaryWorkload = "AuxiliaryWorkload"
)

// AbstractEngine defines Definition's Render interface
type AbstractEngine interface {
	Complete(ctx process.Context, abstractTemplate string, params interface{}) error
	Status(templateContext map[string]interface{}, request *health.StatusRequest) (*health.StatusResult, error)
	GetTemplateContext(ctx process.Context, cli client.Client, accessor util.NamespaceAccessor) (map[string]interface{}, error)
}

type def struct {
	name string
}

type workloadDef struct {
	def
}

// NewWorkloadAbstractEngine create Workload Definition AbstractEngine
func NewWorkloadAbstractEngine(name string) AbstractEngine {
	return &workloadDef{
		def: def{
			name: name,
		},
	}
}

// Complete do workload definition's rendering
func (wd *workloadDef) Complete(ctx process.Context, abstractTemplate string, params interface{}) error {
	var paramFile = velaprocess.ParameterFieldName + ": {}"
	if params != nil {
		bt, err := json.Marshal(params)
		if err != nil {
			return errors.WithMessagef(err, "marshal parameter of workload %s", wd.name)
		}
		if string(bt) != "null" {
			paramFile = fmt.Sprintf("%s: %s", velaprocess.ParameterFieldName, string(bt))
		}
	}

	c, err := ctx.BaseContextFile()
	if err != nil {
		return err
	}

	val, err := cuex.DefaultCompiler.Get().CompileString(ctx.GetCtx(), strings.Join([]string{
		renderTemplate(abstractTemplate), paramFile, c,
	}, "\n"))

	if err != nil {
		return errors.WithMessagef(err, "failed to compile workload %s after merge parameter and context", wd.name)
	}

	if err := val.Validate(); err != nil {
		// Pass the components for rich error context
		components := map[string]string{
			"template": abstractTemplate,
			"params":   paramFile,
			"context":  c,
		}
		return formatCueValidationErrors(err, fmt.Sprintf("workload %s after merge parameter and context", wd.name), components)
	}
	output := val.LookupPath(value.FieldPath(OutputFieldName))
	base, err := model.NewBase(output)
	if err != nil {
		return errors.WithMessagef(err, "invalid output of workload %s", wd.name)
	}
	if err := ctx.SetBase(base); err != nil {
		return err
	}

	// we will support outputs for workload composition, and it will become trait in AppConfig.
	outputs := val.LookupPath(value.FieldPath(OutputsFieldName))
	if !outputs.Exists() {
		return nil
	}
	iter, err := outputs.Fields(cue.Definitions(true), cue.Hidden(true), cue.All())
	if err != nil {
		return errors.WithMessagef(err, "invalid outputs of workload %s", wd.name)
	}
	for iter.Next() {
		if iter.Selector().IsDefinition() || iter.Selector().PkgPath() != "" || iter.IsOptional() {
			continue
		}
		other, err := model.NewOther(iter.Value())
		name := iter.Label()
		if err != nil {
			return errors.WithMessagef(err, "invalid outputs(%s) of workload %s", name, wd.name)
		}
		if err := ctx.AppendAuxiliaries(process.Auxiliary{Ins: other, Type: AuxiliaryWorkload, Name: name}); err != nil {
			return err
		}
	}
	return nil
}

func withCluster(ctx context.Context, o client.Object) context.Context {
	if cluster := oam.GetCluster(o); cluster != "" {
		return multicluster.WithCluster(ctx, cluster)
	}
	return ctx
}

func (wd *workloadDef) getTemplateContext(ctx process.Context, cli client.Reader, accessor util.NamespaceAccessor) (map[string]interface{}, error) {
	baseLabels := GetBaseContextLabels(ctx)
	var root = initRoot(baseLabels)
	var commonLabels = GetCommonLabels(baseLabels)

	base, assists := ctx.Output()
	componentWorkload, err := base.Unstructured()
	if err != nil {
		return nil, err
	}
	// workload main resource will have a unique label("app.oam.dev/resourceType"="WORKLOAD") in per component/app level
	_ctx := withCluster(ctx.GetCtx(), componentWorkload)
	object, err := getResourceFromObj(_ctx, ctx, componentWorkload, cli, accessor.For(componentWorkload), util.MergeMapOverrideWithDst(map[string]string{
		oam.LabelOAMResourceType: oam.ResourceTypeWorkload,
	}, commonLabels), "")
	if err != nil {
		return nil, err
	}
	root[OutputFieldName] = object
	outputs := make(map[string]interface{})
	for _, assist := range assists {
		if assist.Type != AuxiliaryWorkload {
			continue
		}
		if assist.Name == "" {
			return nil, errors.New("the auxiliary of workload must have a name with format 'outputs.<my-name>'")
		}
		traitRef, err := assist.Ins.Unstructured()
		if err != nil {
			return nil, err
		}
		// AuxiliaryWorkload will have a unique label("trait.oam.dev/resource"="name of outputs") in per component/app level
		_ctx := withCluster(ctx.GetCtx(), traitRef)
		object, err := getResourceFromObj(_ctx, ctx, traitRef, cli, accessor.For(traitRef), util.MergeMapOverrideWithDst(map[string]string{
			oam.TraitTypeLabel: AuxiliaryWorkload,
		}, commonLabels), assist.Name)
		if err != nil {
			return nil, err
		}
		outputs[assist.Name] = object
	}
	if len(outputs) > 0 {
		root[OutputsFieldName] = outputs
	}
	return root, nil
}

// Status get workload status by customStatusTemplate
func (wd *workloadDef) Status(templateContext map[string]interface{}, request *health.StatusRequest) (*health.StatusResult, error) {
	return health.GetStatus(templateContext, request)
}

func (wd *workloadDef) GetTemplateContext(ctx process.Context, cli client.Client, accessor util.NamespaceAccessor) (map[string]interface{}, error) {
	return wd.getTemplateContext(ctx, cli, accessor)
}

type traitDef struct {
	def
}

// NewTraitAbstractEngine create Trait Definition AbstractEngine
func NewTraitAbstractEngine(name string) AbstractEngine {
	return &traitDef{
		def: def{
			name: name,
		},
	}
}

// Complete do trait definition's rendering
// nolint:gocyclo
func (td *traitDef) Complete(ctx process.Context, abstractTemplate string, params interface{}) error {
	buff := abstractTemplate + "\n"
	if params != nil {
		bt, err := json.Marshal(params)
		if err != nil {
			return errors.WithMessagef(err, "marshal parameter of trait %s", td.name)
		}
		if string(bt) != "null" {
			buff += fmt.Sprintf("%s: %s\n", velaprocess.ParameterFieldName, string(bt))
		}
	}
	c, err := ctx.BaseContextFile()
	if err != nil {
		return err
	}
	buff += c

	val, err := cuex.DefaultCompiler.Get().CompileString(ctx.GetCtx(), buff)

	if err != nil {
		return errors.WithMessagef(err, "failed to compile trait %s after merge parameter and context", td.name)
	}

	if err := val.Validate(); err != nil {
		// Pass the components for rich error context
		paramStr := ""
		if params != nil {
			if bt, err := json.Marshal(params); err == nil && string(bt) != "null" {
				paramStr = fmt.Sprintf("%s: %s", velaprocess.ParameterFieldName, string(bt))
			}
		}
		components := map[string]string{
			"template": abstractTemplate,
			"params":   paramStr,
			"context":  c,
		}
		return formatCueValidationErrors(err, fmt.Sprintf("trait %s after merge with parameter and context", td.name), components)
	}

	processing := val.LookupPath(value.FieldPath("processing"))
	if processing.Exists() {
		if val, err = task.Process(val); err != nil {
			return errors.WithMessagef(err, "invalid process of trait %s", td.name)
		}
	}
	outputs := val.LookupPath(value.FieldPath(OutputsFieldName))
	if outputs.Exists() {
		iter, err := outputs.Fields(cue.Definitions(true), cue.Hidden(true), cue.All())
		if err != nil {
			return errors.WithMessagef(err, "invalid outputs of trait %s", td.name)
		}
		for iter.Next() {
			if iter.Selector().IsDefinition() || iter.Selector().PkgPath() != "" || iter.IsOptional() {
				continue
			}
			other, err := model.NewOther(iter.Value())
			name := iter.Label()
			if err != nil {
				return errors.WithMessagef(err, "invalid outputs(resource=%s) of trait %s", name, td.name)
			}
			if err := ctx.AppendAuxiliaries(process.Auxiliary{Ins: other, Type: td.name, Name: name}); err != nil {
				return err
			}
		}
	}

	patcher := val.LookupPath(value.FieldPath(PatchFieldName))
	base, auxiliaries := ctx.Output()
	if patcher.Exists() {
		if base == nil {
			return fmt.Errorf("patch trait %s into an invalid workload", td.name)
		}
		if err := base.Unify(patcher, sets.CreateUnifyOptionsForPatcher(patcher)...); err != nil {
			return errors.WithMessagef(err, "invalid patch trait %s into workload", td.name)
		}
	}
	outputsPatcher := val.LookupPath(value.FieldPath(PatchOutputsFieldName))
	if outputsPatcher.Exists() {
		for _, auxiliary := range auxiliaries {
			target := outputsPatcher.LookupPath(value.FieldPath(auxiliary.Name))
			if !target.Exists() {
				continue
			}
			if err = auxiliary.Ins.Unify(target); err != nil {
				return errors.WithMessagef(err, "trait=%s, to=%s, invalid patch trait into auxiliary workload", td.name, auxiliary.Name)
			}
		}
	}

	errs := val.LookupPath(value.FieldPath(ErrsFieldName))
	if errs.Exists() {
		if err := parseErrors(errs); err != nil {
			return err
		}
	}

	return nil
}

func parseErrors(errs cue.Value) error {
	if it, e := errs.List(); e == nil {
		for it.Next() {
			if s, err := it.Value().String(); err == nil && s != "" {
				return errors.Errorf("%s", s)
			}
		}
	}
	return nil
}

// CueValidationError is a custom error type for formatted CUE validation errors
type CueValidationError struct {
	message string
}

func (e *CueValidationError) Error() string {
	return e.message
}

// extractFieldContext attempts to extract useful context from error messages
func extractFieldContext(msg string) (enrichedMsg string, fieldInfo map[string]string) {
	fieldInfo = make(map[string]string)
	enrichedMsg = msg

	// Parse "conflicting values X and Y" to show actual vs expected
	if matches := regexp.MustCompile(`conflicting values (.+) and (.+)`).FindStringSubmatch(msg); len(matches) == 3 {
		fieldInfo["actual"] = matches[1]
		fieldInfo["expected"] = matches[2]
		enrichedMsg = fmt.Sprintf("type mismatch (got %s, expected %s)", matches[1], matches[2])
	}

	// Parse "invalid value X (out of bound Y)" to show the constraint
	if matches := regexp.MustCompile(`invalid value (.+) \(out of bound (.+)\)`).FindStringSubmatch(msg); len(matches) == 3 {
		fieldInfo["value"] = matches[1]
		fieldInfo["constraint"] = matches[2]
		enrichedMsg = fmt.Sprintf("value %s violates constraint %s", matches[1], matches[2])
	}

	// Parse "does not match pattern X" to show the expected pattern
	if matches := regexp.MustCompile(`does not match pattern (.+)`).FindStringSubmatch(msg); len(matches) == 2 {
		fieldInfo["pattern"] = matches[1]
		enrichedMsg = fmt.Sprintf("must match pattern %s", matches[1])
	}

	// Handle "incomplete value X" to suggest what's missing
	if matches := regexp.MustCompile(`incomplete value (.+)`).FindStringSubmatch(msg); len(matches) == 2 {
		fieldInfo["type"] = matches[1]
		enrichedMsg = fmt.Sprintf("missing required %s value", matches[1])
	}

	// Handle "invalid interpolation" - try to make it more specific
	if msg == "invalid interpolation" {
		enrichedMsg = "string interpolation failed (check variable references)"
	}

	// Handle reference errors
	if matches := regexp.MustCompile(`reference "(.+)" not found`).FindStringSubmatch(msg); len(matches) == 2 {
		fieldInfo["missing_ref"] = matches[1]
		enrichedMsg = fmt.Sprintf("undefined reference '%s'", matches[1])
	}

	return enrichedMsg, fieldInfo
}

// replaceValuesWithPlaceholders replaces actual values in error messages with <provided($value)> and <default($value)>
func replaceValuesWithPlaceholders(msg string, fieldInfo map[string]string) string {
	replacedMsg := msg

	// Clean up any trailing cue/format artifacts first
	artifactPattern := regexp.MustCompile(`\s*\(value:\s*cue/format:\s*[^)]*\)\s*$`)
	replacedMsg = artifactPattern.ReplaceAllString(replacedMsg, "")

	// Only replace values in very specific contexts to avoid over-replacement
	provided := strings.Trim(fieldInfo["actual"], `"'`)
	defaultVal := strings.Trim(fieldInfo["default"], `"'`)

	// Skip replacement if we don't have both values
	if provided == "" || defaultVal == "" {
		return replacedMsg
	}

	// Pattern 1: "(got X, expected Y)" format
	gotExpectedPattern := regexp.MustCompile(`\(got\s+([^,\)]+),\s*expected\s+([^,\)]+)\)`)
	if matches := gotExpectedPattern.FindStringSubmatch(replacedMsg); len(matches) == 3 {
		gotValue := strings.TrimSpace(matches[1])
		expectedValue := strings.TrimSpace(matches[2])

		// Replace got value - usually the default or computed value
		var gotReplacement string
		if gotValue == defaultVal {
			gotReplacement = fmt.Sprintf("<default(%s)>", defaultVal)
		} else if gotValue == provided {
			gotReplacement = fmt.Sprintf("<provided(%s)>", provided)
		} else {
			gotReplacement = gotValue
		}

		// Replace expected value - usually the user-provided value
		var expectedReplacement string
		if expectedValue == provided {
			expectedReplacement = fmt.Sprintf("<provided(%s)>", provided)
		} else if expectedValue == defaultVal {
			expectedReplacement = fmt.Sprintf("<default(%s)>", defaultVal)
		} else {
			expectedReplacement = expectedValue
		}

		replacement := fmt.Sprintf("(got %s, expected %s)", gotReplacement, expectedReplacement)
		replacedMsg = gotExpectedPattern.ReplaceAllString(replacedMsg, replacement)
	}

	// Pattern 2: "value X violates constraint" format - only replace the specific value
	valuePattern := regexp.MustCompile(`\bvalue\s+([^\s]+)\s+violates`)
	if matches := valuePattern.FindStringSubmatch(replacedMsg); len(matches) == 2 {
		valueStr := strings.TrimSpace(matches[1])
		if valueStr == provided {
			replacement := fmt.Sprintf("value <provided(%s)> violates", provided)
			replacedMsg = valuePattern.ReplaceAllString(replacedMsg, replacement)
		} else if valueStr == defaultVal {
			replacement := fmt.Sprintf("value <default(%s)> violates", defaultVal)
			replacedMsg = valuePattern.ReplaceAllString(replacedMsg, replacement)
		}
	}

	return replacedMsg
}

// extractValueInfo parses the CUE components to extract actual values and constraints
func extractValueInfo(components map[string]string, path []string) map[string]string {
	info := make(map[string]string)

	// Try to extract parameter value
	if params := components["params"]; params != "" {
		// Parse the parameter CUE/JSON to find the value at the path
		paramVal, err := cuex.DefaultCompiler.Get().CompileString(context.Background(), params)
		if err == nil {
			fieldVal := paramVal.LookupPath(cue.ParsePath(strings.Join(path, ".")))
			if fieldVal.Exists() {
				// Try to get the actual value and determine its type
				if concrete, err := fieldVal.String(); err == nil {
					info["actual"] = fmt.Sprintf("%q", concrete)
					info["provided_type"] = "string"
				} else if num, err := fieldVal.Float64(); err == nil {
					info["actual"] = fmt.Sprintf("%v", num)
					info["provided_type"] = "number"
				} else if num, err := fieldVal.Int64(); err == nil {
					info["actual"] = fmt.Sprintf("%v", num)
					info["provided_type"] = "int"
				} else if b, err := fieldVal.Bool(); err == nil {
					info["actual"] = fmt.Sprintf("%v", b)
					info["provided_type"] = "bool"
				} else {
					// Try to get the source representation
					info["actual"] = fmt.Sprint(fieldVal)
					// Try to infer type from the string representation
					actualStr := strings.Trim(info["actual"], `"'`)
					if actualStr == "true" || actualStr == "false" {
						info["provided_type"] = "bool"
					} else if _, err := strconv.ParseInt(actualStr, 10, 64); err == nil {
						info["provided_type"] = "int"
					} else if _, err := strconv.ParseFloat(actualStr, 64); err == nil {
						info["provided_type"] = "number"
					} else {
						info["provided_type"] = "string"
					}
				}
			}
		}
	}

	// Try to extract template constraints DIRECTLY from the template text first
	if template := components["template"]; template != "" && len(path) > 0 {
		// Direct text parsing - this should always work regardless of CUE compilation
		fieldName := path[len(path)-1]

		// Handle nested paths like [parameter, goodDurationMax]
		// First, find the parameter block if path starts with "parameter"
		searchText := template
		if len(path) > 1 && path[0] == "parameter" {
			// Look for the parameter block - use a more robust pattern
			// This handles multi-line parameter blocks with nested braces
			paramStart := strings.Index(template, "parameter:")
			if paramStart >= 0 {
				// Find the opening brace after parameter:
				afterParam := template[paramStart:]
				braceStart := strings.Index(afterParam, "{")
				if braceStart >= 0 {
					// Count braces to find the matching closing brace
					fullBlock := afterParam[braceStart:]
					braceCount := 0
					endPos := 0
					for i, ch := range fullBlock {
						if ch == '{' {
							braceCount++
						} else if ch == '}' {
							braceCount--
							if braceCount == 0 {
								endPos = i + 1
								break
							}
						}
					}
					if endPos > 0 && endPos < len(fullBlock) {
						searchText = fullBlock[1:endPos-1] // Extract content between braces
					}
				}
			}

			// Fallback to simpler regex if brace counting didn't work
			if searchText == template {
				paramBlockPattern := regexp.MustCompile(`(?ms)parameter:\s*\{([^{}]*(?:\{[^{}]*\}[^{}]*)*)\}`)
				if matches := paramBlockPattern.FindStringSubmatch(template); len(matches) > 1 {
					searchText = matches[1]
				}
			}
		}

		// Build regex to match field definition
		// Handle both indented (with tabs/spaces) and non-indented lines
		patternStr := `(?m)^\s*` + regexp.QuoteMeta(fieldName) + `:\s*(.+?)$`
		pattern := regexp.MustCompile(patternStr)

		if matches := pattern.FindStringSubmatch(searchText); len(matches) > 1 {
			definition := strings.TrimSpace(matches[1])
			info["definition"] = definition

			// Extract default from patterns like "*60 | int & >=10" or "*\"string\" | string"
			if strings.Contains(definition, "*") {
				// Handle various default patterns: *60, *"string", *true, etc.
				// Match everything after * up to | or & or end of line
				defaultPattern := regexp.MustCompile(`\*([^|&]+)`)
				if defaultMatch := defaultPattern.FindStringSubmatch(definition); len(defaultMatch) > 1 {
					defaultVal := strings.TrimSpace(defaultMatch[1])
					// Clean up quotes but preserve them if they're part of the value
					if (strings.HasPrefix(defaultVal, `"`) && strings.HasSuffix(defaultVal, `"`)) ||
					   (strings.HasPrefix(defaultVal, `'`) && strings.HasSuffix(defaultVal, `'`)) {
						// Keep quotes for string values
						info["default"] = defaultVal
					} else {
						// Remove any trailing spaces or special characters
						defaultVal = strings.TrimSpace(defaultVal)
						info["default"] = defaultVal
					}
				}
			}

			// Extract constraints from patterns like "int & >=10" or "string & len(<5)"
			constraintPattern := regexp.MustCompile(`(?:int|string|bool|number|float)\s*&\s*(.+?)(?:\s*$|\s*\|)`)
			if constraintMatch := constraintPattern.FindStringSubmatch(definition); len(constraintMatch) > 1 {
				info["constraint"] = strings.TrimSpace(constraintMatch[1])
			}

			// Extract expected type from patterns like "*60 | int" or "int & >=10"
			typePattern := regexp.MustCompile(`(?:\*[^|]*\s*\|\s*)?(int|string|bool|number|float)`)
			if typeMatch := typePattern.FindStringSubmatch(definition); len(typeMatch) > 1 {
				info["expected_type"] = typeMatch[1]
			}
		}
	}

	// Also try to extract via CUE compilation (as fallback/additional info)
	if template := components["template"]; template != "" {
		// Parse template to find constraints
		templateVal, err := cuex.DefaultCompiler.Get().CompileString(context.Background(), template)
		if err == nil {
			fieldDef := templateVal.LookupPath(cue.ParsePath(strings.Join(path, ".")))
			if fieldDef.Exists() && info["definition"] == "" {
				// Fallback: try to get definition from compiled CUE

				// If we didn't get the definition from text parsing, use the compiled value
				if _, hasDefinition := info["definition"]; !hasDefinition {
					// Extract the source representation of the field
					srcStr := fmt.Sprint(fieldDef)

					// Try to get the formatted source if available
					if src, err := format.Node(fieldDef.Source()); err == nil && len(src) > 0 {
						srcStr = string(src)
					}

					// Clean up and extract the definition
					if srcStr != "" && srcStr != "_" {
						// Remove field name prefix if present
						if idx := strings.LastIndex(srcStr, ":"); idx > 0 {
							beforeColon := srcStr[:idx]
							if !strings.Contains(beforeColon, "&") && !strings.Contains(beforeColon, "|") {
								srcStr = strings.TrimSpace(srcStr[idx+1:])
							}
						}
						info["definition"] = srcStr
					}
				}

				// Also try the Default() method as fallback
				if _, hasDefault := info["default"]; !hasDefault {
					if def, hasDefault := fieldDef.Default(); hasDefault {
						if s, err := def.String(); err == nil {
							info["default"] = fmt.Sprintf("%q", s)
						} else if num, err := def.Int64(); err == nil {
							info["default"] = fmt.Sprintf("%d", num)
						} else if num, err := def.Float64(); err == nil {
							info["default"] = fmt.Sprintf("%g", num)
						} else {
							info["default"] = fmt.Sprint(def)
						}
					}
				}

				// Try to extract type information
				if fieldDef.IncompleteKind() != cue.BottomKind {
					info["type"] = fieldDef.IncompleteKind().String()
				}
			}
		}
	}

	// Final debug output
	fmt.Printf("Final extracted info: %+v\n", info)
	fmt.Printf("===========================\n")

	return info
}

// formatCueValidationErrors formats CUE validation errors in a user-friendly way
func formatCueValidationErrors(err error, context string, components map[string]string) error {
	if err == nil {
		return nil
	}

	cueErrs := cueErrors.Errors(err)

	// Group errors by path and deduplicate
	type errorDetail struct {
		message string
		count   int
		info    map[string]string
	}
	errorGroups := make(map[string][]errorDetail) // path -> list of error details
	errorIndex := make(map[string]map[string]int) // path -> message -> index
	var orderedPaths []string

	for _, cueErr := range cueErrs {
		path := cueErr.Path()
		format, args := cueErr.Msg()
		msg := fmt.Sprintf(format, args...)

		// DEBUG: Show what we actually get from CUE
		// fmt.Printf("DEBUG - Path: %v, Format: %q, Args: %v, Final: %q\n", path, format, args, msg)

		// Convert path (which is []string) to a string representation
		pathStr := ""
		if len(path) > 0 {
			pathStr = strings.Join(path, ".")
		} else {
			pathStr = "(root)"
		}

		// Check if this is a disjunction error that will have sub-errors
		if strings.Contains(msg, "errors in empty disjunction") {
			// Skip this parent error as we'll show the detailed sub-errors
			continue
		}

		// Track order of first appearance
		if _, exists := errorGroups[pathStr]; !exists {
			orderedPaths = append(orderedPaths, pathStr)
			errorGroups[pathStr] = []errorDetail{}
			errorIndex[pathStr] = make(map[string]int)
		}

		// Enrich the error message
		enrichedMsg, fieldInfo := extractFieldContext(msg)

		// Extract actual values from the CUE components
		valueInfo := extractValueInfo(components, path)

		// Merge the extracted value info with field info
		for k, v := range valueInfo {
			if _, exists := fieldInfo[k]; !exists {
				fieldInfo[k] = v
			}
		}

		// Replace actual values with placeholders in the enriched message
		enrichedMsg = replaceValuesWithPlaceholders(enrichedMsg, fieldInfo)

		// Check if we already have this error
		if idx, exists := errorIndex[pathStr][msg]; exists {
			errorGroups[pathStr][idx].count++
		} else {
			errorIndex[pathStr][msg] = len(errorGroups[pathStr])
			errorGroups[pathStr] = append(errorGroups[pathStr], errorDetail{
				message: enrichedMsg,
				count:   1,
				info:    fieldInfo,
			})
		}
	}

	// Format the errors in structured multi-line format
	var formattedErrors []string

	for _, pathStr := range orderedPaths {
		errors := errorGroups[pathStr]

		// Collect all unique info across all errors for this field
		allInfo := make(map[string]string)
		var errorMessages []string

		for _, err := range errors {
			// Collect error messages
			if err.count > 1 {
				errorMessages = append(errorMessages, fmt.Sprintf("%s (×%d)", err.message, err.count))
			} else {
				errorMessages = append(errorMessages, err.message)
			}

			// Merge all info (later errors may have more complete info)
			for k, v := range err.info {
				allInfo[k] = v
			}
		}

		// Format the field block
		formattedErrors = append(formattedErrors, fmt.Sprintf("\n[%s]", pathStr))

		// Add statement/definition if available
		if val, ok := allInfo["definition"]; ok && val != "" {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  statement:    %s", val))
		} else if val, ok := allInfo["type"]; ok && val != "" {
			// Fallback to type if no full definition
			formattedErrors = append(formattedErrors, fmt.Sprintf("  statement:    %s", val))
		}

		// Add default value if available
		if val, ok := allInfo["default"]; ok {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  default:      %s", val))
		}

		// Add provided value if available
		if val, ok := allInfo["actual"]; ok {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  provided:     %s", val))
		}

		// Add provided type if available
		if val, ok := allInfo["provided_type"]; ok {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  provided type: %s", val))
		}

		// Add expected type if available
		if val, ok := allInfo["expected_type"]; ok {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  expected type: %s", val))
		}

		// Add constraints if available and not already in statement
		constraints := []string{}
		if val, ok := allInfo["constraint"]; ok && !strings.Contains(allInfo["definition"], val) {
			constraints = append(constraints, val)
		}
		if val, ok := allInfo["pattern"]; ok && !strings.Contains(allInfo["definition"], val) {
			constraints = append(constraints, fmt.Sprintf("pattern: %s", val))
		}
		if len(constraints) > 0 {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  constraints:  %s", strings.Join(constraints, ", ")))
		}

		// Add error messages
		if len(errorMessages) == 1 {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  error:        %s", errorMessages[0]))
		} else {
			formattedErrors = append(formattedErrors, fmt.Sprintf("  errors:       [%s]", strings.Join(errorMessages, ", ")))
		}
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("CUE validation failed for %s:\n", context))
	result.WriteString(strings.Join(formattedErrors, "\n"))

	return &CueValidationError{message: result.String()}
}

// GetCommonLabels will convert context based labels to OAM standard labels
func GetCommonLabels(contextLabels map[string]string) map[string]string {
	var commonLabels = map[string]string{}
	for k, v := range contextLabels {
		switch k {
		case velaprocess.ContextAppName:
			commonLabels[oam.LabelAppName] = v
		case velaprocess.ContextName:
			commonLabels[oam.LabelAppComponent] = v
		case velaprocess.ContextAppRevision:
			commonLabels[oam.LabelAppRevision] = v
		case velaprocess.ContextReplicaKey:
			commonLabels[oam.LabelReplicaKey] = v

		}
	}
	return commonLabels
}

// GetBaseContextLabels get base context labels
func GetBaseContextLabels(ctx process.Context) map[string]string {
	baseLabels := ctx.BaseContextLabels()
	baseLabels[velaprocess.ContextAppName] = ctx.GetData(velaprocess.ContextAppName).(string)
	baseLabels[velaprocess.ContextAppRevision] = ctx.GetData(velaprocess.ContextAppRevision).(string)

	return baseLabels
}

func initRoot(contextLabels map[string]string) map[string]interface{} {
	var root = map[string]interface{}{}
	for k, v := range contextLabels {
		root[k] = v
	}
	return root
}

func renderTemplate(templ string) string {
	return templ + `
context: _
parameter: _
`
}

func (td *traitDef) getTemplateContext(ctx process.Context, cli client.Reader, accessor util.NamespaceAccessor) (map[string]interface{}, error) {
	baseLabels := GetBaseContextLabels(ctx)
	var root = initRoot(baseLabels)
	var commonLabels = GetCommonLabels(baseLabels)

	_, assists := ctx.Output()
	outputs := make(map[string]interface{})
	for _, assist := range assists {
		if assist.Type != td.name {
			continue
		}
		traitRef, err := assist.Ins.Unstructured()
		if err != nil {
			return nil, err
		}
		_ctx := withCluster(ctx.GetCtx(), traitRef)
		object, err := getResourceFromObj(_ctx, ctx, traitRef, cli, accessor.For(traitRef), util.MergeMapOverrideWithDst(map[string]string{
			oam.TraitTypeLabel: assist.Type,
		}, commonLabels), assist.Name)
		if err != nil {
			return nil, err
		}
		outputs[assist.Name] = object
	}
	if len(outputs) > 0 {
		root[OutputsFieldName] = outputs
	}
	return root, nil
}

// Status get trait status by customStatusTemplate
func (td *traitDef) Status(templateContext map[string]interface{}, request *health.StatusRequest) (*health.StatusResult, error) {
	return health.GetStatus(templateContext, request)
}

func (td *traitDef) GetTemplateContext(ctx process.Context, cli client.Client, accessor util.NamespaceAccessor) (map[string]interface{}, error) {
	return td.getTemplateContext(ctx, cli, accessor)
}

func getResourceFromObj(ctx context.Context, pctx process.Context, obj *unstructured.Unstructured, client client.Reader, namespace string, labels map[string]string, outputsResource string) (map[string]interface{}, error) {
	if outputsResource != "" {
		labels[oam.TraitResource] = outputsResource
	}
	if obj.GetName() != "" {
		u, err := util.GetObjectGivenGVKAndName(ctx, client, obj.GroupVersionKind(), namespace, obj.GetName())
		if err != nil {
			return nil, err
		}
		return u.Object, nil
	}
	if ctxName := pctx.GetData(model.ContextName).(string); ctxName != "" {
		u, err := util.GetObjectGivenGVKAndName(ctx, client, obj.GroupVersionKind(), namespace, ctxName)
		if err == nil {
			return u.Object, nil
		}
	}
	list, err := util.GetObjectsGivenGVKAndLabels(ctx, client, obj.GroupVersionKind(), namespace, labels)
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 1 {
		return list.Items[0].Object, nil
	}
	for _, v := range list.Items {
		if v.GetLabels()[oam.TraitResource] == outputsResource {
			return v.Object, nil
		}
	}
	return nil, errors.Errorf("no resources found gvk(%v) labels(%v)", obj.GroupVersionKind(), labels)
}
