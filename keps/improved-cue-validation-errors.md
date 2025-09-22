# KEP: Improved CUE Validation Error Messages

## Summary

This KEP proposes enhancing CUE validation error messages in KubeVela to provide users with clear, structured, and actionable feedback when their configurations fail validation. The current error messages are cryptic and difficult to interpret; leading to poor DevEx, increased debugging time, and frustration.

## Motivation

Currently, when CUE validation fails in KubeVela, users receive aggregated error messages like:
```
conflicting values 1 and 60 (and 54 more errors):
```

These messages lack critical context that users need to fix their configurations:
- What field has the error
- What value was expected vs. provided
- What the default value is
- What constraints were violated
- What types were expected vs. provided
- What the _"n more errors"_ contains (the reported error often masks the actual issue)

This poor error experience leads to:
- Increased time to resolve configuration issues
- User frustration and abandonment
- Difficulty debugging complex CUE templates

### Goals

1. **Provide structured error messages** that clearly identify the field, expected values, provided values, and constraints
2. **Extract and display CUE template metadata** including defaults, types, and constraints
3. **Deduplicate repetitive errors** while maintaining count information
4. **Replace cryptic values with meaningful placeholders** like `<provided(1)>` and `<default(60)>`
5. **Remove CUE internal artifacts** that confuse users

### Non-Goals

1. Modifying the CUE validation logic itself
2. Changing how CUE templates are written or structured
3. Providing automatic fixes for validation errors

## Proposal

### User Stories

#### Story 1: KubeVela User (Developering or Debugging Parameter Validation)
As a DevOps engineer, when I provide an invalid parameter value to a KubeVela application, I want to see:
- The exact parameter/s that failed
- The source of the values provided in the error message (default vs. provided)
- What the default value is
- What constraints my value violated
- The expected type vs what I provided

So that I can quickly fix my configuration without needing to read and debug the entire CUE template.

### Design Details

**POC**: https://github.com/briankane/kubevela/pull/new/kep/0003-cue-error-reporting

#### 1. Error Message Structure

Transform errors into a structured format:

*Example Proposed Structure (as demonstrated in POC)*:
```
[parameter.fieldName]
  statement:     *60 | int & >=10           # Full CUE definition
  default:       60                         # Default value from template
  provided:      1 (int)                    # User-provided value
  expected type: int                        # Expected type from template
  constraints:   >=10                       # Extracted constraints
  error:         type mismatch (got <default(60)>, expected <provided(1)>),
                 value <provided(1)> violates constraint >=10
```

#### 2. Core Capabilities Required

**a. Error Organization and Presentation**
- Group related errors by their field paths for clarity
- Eliminate duplicate error messages while tracking frequency
- Present errors in a consistent, structured format
- Preserve the logical flow of error discovery

**b. Contextual Information Extraction**
- Identify and extract meaningful patterns from CUE error messages
- Recognize common error types (value conflicts, type mismatches, constraint violations)
- Transform technical error messages into user-friendly descriptions
- Preserve technical details while adding human-readable context

**c. Template Metadata Discovery**
- Extract field definitions from CUE templates
- Identify default values when specified
- Determine expected types and constraints
- Correlate user-provided values with template expectations 

**d. Error Message Enhancement**
- Clearly distinguish between different value sources (user-provided vs defaults)
- Remove internal implementation details that don't help users
- Add semantic meaning to values in error messages
- Maintain accuracy while improving readability

#### 3. Implementation Approach

**Phase 1: String-Based Error Formatting (v1.12)**
- Implement comprehensive CUE error formatting and enrichment
- Extract all contextual information from templates and error messages
- Provide structured, human-readable error output
- Persist formatted errors as multi-line strings in existing message fields
- Maintain compatibility with current error handling
- Gather user feedback and iterate on formatting

**Phase 2: Structured Error Storage (on promotion of Phase 1 to beta; ~v1.13)**
- Replace string-based formatting with structured error objects
- Add dedicated errors field to the Application CRD status
- Store validation errors as structured data (arrays/maps)
- Enable programmatic access and parsing by external systems
- Support filtering, querying, and analysis of error details
- Maintain backwards compatibility by preserving message field

### Test Plan

#### Unit Tests
1. Test error formatting with various CUE error types
2. Test template parsing with complex nested structures
3. Test constraint extraction from different patterns
4. Test value replacement accuracy
5. Test artifact removal

#### Integration Tests
1. Test with real KubeVela workload definitions
2. Test with trait definitions
3. Test with complex multi-field errors
4. Test with deeply nested parameter structures

#### Example Test Cases

```go
// Test constraint extraction
input: "*60 | int & >=10"
expected: {
  default: "60",
  type: "int",
  constraint: ">=10"
}

// Test value replacement
input: "type mismatch (got 45, expected 1)"
provided: "1"
providedType: int
expectedType: int
default: "45"
expected: "type mismatch (got <default(45)>, expected <provided(1)>)"
```

## Implementation History

- 2024-01-XX: Initial KEP proposal
- 2024-01-XX: Implementation of core error formatting
- 2024-01-XX: Addition of template parsing and value extraction
- 2024-01-XX: Implementation of value replacement and type detection

## Drawbacks

1. **Increased complexity** in error handling code
2. **Regex-based parsing** may need updates if CUE error formats change
3. **Performance impact** from additional parsing and formatting (minimal)
4. **Maintenance burden** of keeping parsing logic in sync with CUE updates

## Alternatives Considered

### Update Cue Version
- Provides complimentary features (e.g. custom errors)
- Does not replace the need for more KubeVela focused, user friendly error reporting

### Contribute to CUE
- Features required are very KubeVela focused (template parsing, context awareness etc.)
- Goes against CUE community guidelines re. errors

### 4. AI/LLM-Based Error Enhancement
- LLM too non-deterministic
- LLM often struggle with CUE syntax
- If errors are eventually persisted to the Application in structured format, AIs/MLs will be able to consume downstream from there to support development / usage

## References

- [CUE Language Specification](https://cuelang.org/docs/references/spec/)
- [KubeVela CUE Integration Documentation](https://kubevela.io/docs/platform-engineers/cue/basic)