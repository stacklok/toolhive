// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"
)

// Note: context.Context is included in function signatures for future use
// (e.g., for cancellation of long-running template expansion).
// Currently unused but maintained for interface compatibility.

const (
	// maxTemplateDepth is the maximum recursion depth for template expansion.
	// This prevents stack overflow from deeply nested objects.
	maxTemplateDepth = 100

	// maxTemplateOutputSize is the maximum size in bytes for template expansion output.
	// This prevents memory exhaustion from maliciously large template outputs.
	maxTemplateOutputSize = 10 * 1024 * 1024 // 10 MB
)

// defaultTemplateExpander implements TemplateExpander using Go's text/template.
type defaultTemplateExpander struct {
	// funcMap provides custom template functions.
	funcMap template.FuncMap
}

// NewTemplateExpander creates a new template expander.
func NewTemplateExpander() TemplateExpander {
	return &defaultTemplateExpander{
		funcMap: template.FuncMap{
			"json": jsonEncode,
			"quote": func(s string) string {
				return fmt.Sprintf("%q", s)
			},
		},
	}
}

// Expand evaluates templates in the given data using the workflow context.
// It recursively processes all string values and expands templates.
func (e *defaultTemplateExpander) Expand(
	ctx context.Context,
	data map[string]any,
	workflowCtx *WorkflowContext,
) (map[string]any, error) {
	if data == nil {
		return nil, nil
	}

	result := make(map[string]any, len(data))
	for key, value := range data {
		expanded, err := e.expandValue(ctx, value, workflowCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to expand value for key %q: %w", key, err)
		}
		result[key] = expanded
	}

	return result, nil
}

// expandValue recursively expands templates in a value.
func (e *defaultTemplateExpander) expandValue(
	ctx context.Context,
	value any,
	workflowCtx *WorkflowContext,
) (any, error) {
	return e.expandValueWithDepth(ctx, value, workflowCtx, 0)
}

// expandValueWithDepth recursively expands templates with depth tracking.
func (e *defaultTemplateExpander) expandValueWithDepth(
	ctx context.Context,
	value any,
	workflowCtx *WorkflowContext,
	depth int,
) (any, error) {
	// Prevent stack overflow from deeply nested templates
	if depth > maxTemplateDepth {
		return nil, fmt.Errorf("template expansion depth limit exceeded: %d", maxTemplateDepth)
	}
	switch v := value.(type) {
	case string:
		// Expand template string
		return e.expandString(ctx, v, workflowCtx)

	case map[string]any:
		// Recursively expand nested maps
		expanded := make(map[string]any, len(v))
		for key, val := range v {
			expandedVal, err := e.expandValueWithDepth(ctx, val, workflowCtx, depth+1)
			if err != nil {
				return nil, fmt.Errorf("failed to expand nested key %q: %w", key, err)
			}
			expanded[key] = expandedVal
		}
		return expanded, nil

	case []any:
		// Recursively expand arrays
		expanded := make([]any, len(v))
		for i, val := range v {
			expandedVal, err := e.expandValueWithDepth(ctx, val, workflowCtx, depth+1)
			if err != nil {
				return nil, fmt.Errorf("failed to expand array element %d: %w", i, err)
			}
			expanded[i] = expandedVal
		}
		return expanded, nil

	default:
		// Return other types unchanged (numbers, booleans, nil)
		return value, nil
	}
}

// expandString expands a single template string.
func (e *defaultTemplateExpander) expandString(
	_ context.Context,
	tmplStr string,
	workflowCtx *WorkflowContext,
) (string, error) {
	// Create template context with params and steps
	tmplCtx := map[string]any{
		"params": workflowCtx.Params,
		"steps":  e.buildStepsContext(workflowCtx),
		"vars":   workflowCtx.Variables,
	}

	// Parse and execute template
	tmpl, err := template.New("expand").Funcs(e.funcMap).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	// Pre-allocate reasonable buffer size to reduce allocations
	buf.Grow(1024)

	if err := tmpl.Execute(&buf, tmplCtx); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	// Enforce output size limit to prevent memory exhaustion
	if buf.Len() > maxTemplateOutputSize {
		return "", fmt.Errorf("template output too large: %d bytes (max %d)",
			buf.Len(), maxTemplateOutputSize)
	}

	return buf.String(), nil
}

// buildStepsContext converts StepResult map to a template-friendly structure.
// This provides access to step outputs via {{.steps.stepid.output.field}}.
func (*defaultTemplateExpander) buildStepsContext(workflowCtx *WorkflowContext) map[string]any {
	stepsCtx := make(map[string]any, len(workflowCtx.Steps))

	for stepID, result := range workflowCtx.Steps {
		stepData := map[string]any{
			"status": string(result.Status),
			"output": result.Output,
		}

		// Add error information if step failed
		if result.Error != nil {
			stepData["error"] = result.Error.Error()
		}

		stepsCtx[stepID] = stepData
	}

	return stepsCtx
}

// EvaluateCondition evaluates a condition template to a boolean.
// The condition string must evaluate to "true" or "false".
func (e *defaultTemplateExpander) EvaluateCondition(
	_ context.Context,
	condition string,
	workflowCtx *WorkflowContext,
) (bool, error) {
	if condition == "" {
		return true, nil
	}

	// Expand the condition as a template
	result, err := e.expandString(context.TODO(), condition, workflowCtx)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate condition: %w", err)
	}

	// Parse as boolean
	switch result {
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	default:
		return false, fmt.Errorf("condition must evaluate to 'true' or 'false', got: %q", result)
	}
}

// jsonEncode is a template function that encodes a value as JSON.
func jsonEncode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to encode JSON: %w", err)
	}
	return string(b), nil
}
