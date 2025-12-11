package v1alpha1

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/templates"
)

// stepFieldRef represents a reference to a specific field on a step's output.
type stepFieldRef struct {
	stepID string
	field  string
}

// validateDefaultResultsForSteps validates that defaultResults is specified for steps that:
// 1. May be skipped (have a condition or onError.action == "continue")
// 2. Are referenced by downstream steps
//
// This is a shared validation function used by both VirtualMCPServer and VirtualMCPCompositeToolDefinition webhooks.
// The pathPrefix parameter allows customizing error message paths (e.g., "spec.steps" or "spec.compositeTools[0].steps").
// nolint:gocyclo // multiple passes of the workflow are required to validate references are safe.
func validateDefaultResultsForSteps(pathPrefix string, steps []WorkflowStep, output *OutputSpec) error {
	// 1. Compute all skippable step IDs
	skippableStepIDs := make(map[string]struct{})
	for _, step := range steps {
		if stepMayBeSkipped(step) {
			skippableStepIDs[step.ID] = struct{}{}
		}
	}

	// If no skippable steps, nothing to validate
	if len(skippableStepIDs) == 0 {
		return nil
	}

	// 2. Compute map from skippable step ID to set of fields with default values
	skippableStepDefaults := make(map[string]map[string]struct{})
	for _, step := range steps {
		if _, ok := skippableStepIDs[step.ID]; ok {
			skippableStepDefaults[step.ID] = make(map[string]struct{})
			for key := range step.DefaultResults {
				skippableStepDefaults[step.ID][key] = struct{}{}
			}
		}
	}

	// 3. For each step, check if any references are to skippable steps missing defaults for that field
	for _, step := range steps {
		refs, err := extractStepFieldRefsFromStep(step)
		if err != nil {
			return fmt.Errorf("failed to extract step references from step %s: %w", step.ID, err)
		}

		for _, ref := range refs {
			// Check if this step is skippable
			defaultFields, isSkippable := skippableStepDefaults[ref.stepID]
			if !isSkippable {
				continue
			}

			// Check if the referenced field has a default
			if _, hasDefault := defaultFields[ref.field]; !hasDefault {
				return fmt.Errorf(
					"%s[%s].defaultResults[%s] is required: step %q may be skipped and field %q is referenced by step %s",
					pathPrefix, ref.stepID, ref.field, ref.stepID, ref.field, step.ID)
			}
		}
	}

	// Check output for references to skippable steps missing defaults
	if output != nil {
		outputRefs, err := extractStepFieldRefsFromOutput(output)
		if err != nil {
			return fmt.Errorf("failed to extract step references from output: %w", err)
		}

		for _, ref := range outputRefs {
			defaultFields, isSkippable := skippableStepDefaults[ref.stepID]
			if !isSkippable {
				continue
			}

			if _, hasDefault := defaultFields[ref.field]; !hasDefault {
				return fmt.Errorf(
					"%s[%s].defaultResults[%s] is required: step %q may be skipped and field %q is referenced by output",
					pathPrefix, ref.stepID, ref.field, ref.stepID, ref.field)
			}
		}
	}

	return nil
}

// stepMayBeSkipped returns true if a step may be skipped during execution.
// A step may be skipped if:
// - It has a condition (may evaluate to false)
// - It has onError.action == "continue" (may fail and be skipped)
func stepMayBeSkipped(step WorkflowStep) bool {
	// Step has a condition that may evaluate to false
	if step.Condition != "" {
		return true
	}

	// Step has continue-on-error, meaning failure results in skip
	if step.OnError != nil && step.OnError.Action == ErrorActionContinue {
		return true
	}

	return false
}

// extractStepFieldRefsFromStep extracts step field references from a step's templates.
func extractStepFieldRefsFromStep(step WorkflowStep) ([]stepFieldRef, error) {
	var allRefs []stepFieldRef

	// Extract from condition
	if step.Condition != "" {
		refs, err := extractStepFieldRefsFromTemplate(step.Condition)
		if err != nil {
			return nil, err
		}
		allRefs = append(allRefs, refs...)
	}

	// Extract from arguments
	if step.Arguments != nil && len(step.Arguments.Raw) > 0 {
		var args map[string]any
		if err := json.Unmarshal(step.Arguments.Raw, &args); err == nil {
			for _, argValue := range args {
				if strValue, ok := argValue.(string); ok {
					refs, err := extractStepFieldRefsFromTemplate(strValue)
					if err != nil {
						return nil, err
					}
					allRefs = append(allRefs, refs...)
				}
			}
		}
	}

	// Extract from message (elicitation steps)
	if step.Message != "" {
		refs, err := extractStepFieldRefsFromTemplate(step.Message)
		if err != nil {
			return nil, err
		}
		allRefs = append(allRefs, refs...)
	}

	return uniqueStepFieldRefs(allRefs), nil
}

// extractStepFieldRefsFromOutput extracts step field references from output templates.
func extractStepFieldRefsFromOutput(output *OutputSpec) ([]stepFieldRef, error) {
	if output == nil {
		return nil, nil
	}

	var allRefs []stepFieldRef

	for _, prop := range output.Properties {
		if prop.Value != "" {
			refs, err := extractStepFieldRefsFromTemplate(prop.Value)
			if err != nil {
				return nil, err
			}
			allRefs = append(allRefs, refs...)
		}

		// Recursively check nested properties
		if len(prop.Properties) > 0 {
			nestedOutput := &OutputSpec{Properties: prop.Properties}
			nestedRefs, err := extractStepFieldRefsFromOutput(nestedOutput)
			if err != nil {
				return nil, err
			}
			allRefs = append(allRefs, nestedRefs...)
		}
	}

	return uniqueStepFieldRefs(allRefs), nil
}

// extractStepFieldRefsFromTemplate extracts step output field references from a template string.
// Only references to .steps.<stepID>.output.<field> are extracted.
// For ".steps.step1.output.foo.bar", it returns stepFieldRef{stepID: "step1", field: "foo"}.
// References to .steps.<stepID>.status or .steps.<stepID>.error are ignored.
func extractStepFieldRefsFromTemplate(tmplStr string) ([]stepFieldRef, error) {
	refs, err := templates.ExtractReferences(tmplStr)
	if err != nil {
		return nil, err
	}

	var stepRefs []stepFieldRef
	for _, ref := range refs {
		// Look for ".steps.<stepID>.output.<field>" pattern
		if strings.HasPrefix(ref, ".steps.") {
			// Split: ["", "steps", "stepID", "output", "field", ...]
			parts := strings.SplitN(ref, ".", 6)
			// Must have at least 5 parts and the 4th must be "output"
			if len(parts) >= 5 && parts[3] == "output" {
				stepRefs = append(stepRefs, stepFieldRef{
					stepID: parts[2],
					field:  parts[4],
				})
			}
		}
	}

	return uniqueStepFieldRefs(stepRefs), nil
}

// uniqueStepFieldRefs returns a deduplicated slice of stepFieldRefs.
func uniqueStepFieldRefs(refs []stepFieldRef) []stepFieldRef {
	seen := make(map[stepFieldRef]struct{})
	result := make([]stepFieldRef, 0, len(refs))
	for _, r := range refs {
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			result = append(result, r)
		}
	}
	return result
}
