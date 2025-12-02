package v1alpha1

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the webhook with the manager
func (r *VirtualMCPServer) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=create;update,versions=v1alpha1,name=vvirtualmcpserver.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &VirtualMCPServer{}

// ValidateCreate implements webhook.CustomValidator
func (r *VirtualMCPServer) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateUpdate implements webhook.CustomValidator
func (r *VirtualMCPServer) ValidateUpdate(_ context.Context, _ runtime.Object, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateDelete implements webhook.CustomValidator
func (*VirtualMCPServer) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}

// Validate performs validation for VirtualMCPServer
// This method can be called by the controller during reconciliation or by the webhook
func (r *VirtualMCPServer) Validate() error {
	// Validate GroupRef is set (required field)
	if r.Spec.GroupRef.Name == "" {
		return fmt.Errorf("spec.groupRef.name is required")
	}

	// Validate IncomingAuth configuration
	if r.Spec.IncomingAuth != nil {
		if err := r.validateIncomingAuth(); err != nil {
			return err
		}
	}

	// Validate OutgoingAuth configuration
	if r.Spec.OutgoingAuth != nil {
		if err := r.validateOutgoingAuth(); err != nil {
			return err
		}
	}

	// Validate Aggregation configuration
	if r.Spec.Aggregation != nil {
		if err := r.validateAggregation(); err != nil {
			return err
		}
	}

	// Validate CompositeTools
	if len(r.Spec.CompositeTools) > 0 {
		if err := r.validateCompositeTools(); err != nil {
			return err
		}
	}

	return nil
}

// validateIncomingAuth validates IncomingAuth configuration
func (r *VirtualMCPServer) validateIncomingAuth() error {
	auth := r.Spec.IncomingAuth

	// Type is required when IncomingAuth is specified
	if auth.Type == "" {
		return fmt.Errorf("spec.incomingAuth.type is required")
	}

	// Validate type-specific requirements
	if auth.Type == "oidc" && auth.OIDCConfig == nil {
		return fmt.Errorf("spec.incomingAuth.oidcConfig is required when type is oidc")
	}

	return nil
}

// validateOutgoingAuth validates OutgoingAuth configuration
func (r *VirtualMCPServer) validateOutgoingAuth() error {
	auth := r.Spec.OutgoingAuth

	// Validate source enum values (already validated by kubebuilder but being explicit)
	validSources := map[string]bool{
		"discovered": true,
		"inline":     true,
	}
	if auth.Source != "" && !validSources[auth.Source] {
		return fmt.Errorf("spec.outgoingAuth.source must be one of: discovered, inline")
	}

	// Validate backend configurations
	for backendName, backendAuth := range auth.Backends {
		if err := r.validateBackendAuth(backendName, backendAuth); err != nil {
			return err
		}
	}

	return nil
}

// validateBackendAuth validates a single backend auth configuration
func (*VirtualMCPServer) validateBackendAuth(backendName string, auth BackendAuthConfig) error {
	// Validate type is set
	if auth.Type == "" {
		return fmt.Errorf("spec.outgoingAuth.backends[%s].type is required", backendName)
	}

	// Validate type-specific configurations
	switch auth.Type {
	case BackendAuthTypeExternalAuthConfigRef:
		if auth.ExternalAuthConfigRef == nil {
			return fmt.Errorf(
				"spec.outgoingAuth.backends[%s].externalAuthConfigRef is required when type is external_auth_config_ref",
				backendName)
		}
		if auth.ExternalAuthConfigRef.Name == "" {
			return fmt.Errorf("spec.outgoingAuth.backends[%s].externalAuthConfigRef.name is required", backendName)
		}

	case BackendAuthTypeDiscovered:
		// No additional validation needed

	default:
		return fmt.Errorf(
			"spec.outgoingAuth.backends[%s].type must be one of: discovered, external_auth_config_ref",
			backendName)
	}

	return nil
}

// validateAggregation validates Aggregation configuration
func (r *VirtualMCPServer) validateAggregation() error {
	agg := r.Spec.Aggregation

	// Validate conflict resolution strategy
	if agg.ConflictResolution != "" {
		validStrategies := map[string]bool{
			ConflictResolutionPrefix:   true,
			ConflictResolutionPriority: true,
			ConflictResolutionManual:   true,
		}
		if !validStrategies[agg.ConflictResolution] {
			return fmt.Errorf("spec.aggregation.conflictResolution must be one of: prefix, priority, manual")
		}
	}

	// Validate conflict resolution config based on strategy
	if agg.ConflictResolutionConfig != nil {
		config := agg.ConflictResolutionConfig

		switch agg.ConflictResolution {
		case ConflictResolutionPriority:
			if len(config.PriorityOrder) == 0 {
				return fmt.Errorf("spec.aggregation.conflictResolutionConfig.priorityOrder is required when conflictResolution is priority")
			}

		case ConflictResolutionManual:
			// For manual resolution, tools must define explicit overrides
			// This will be validated at runtime when conflicts are detected
		}
	}

	// Validate per-workload tool configurations
	for i, toolConfig := range agg.Tools {
		if toolConfig.Workload == "" {
			return fmt.Errorf("spec.aggregation.tools[%d].workload is required", i)
		}

		// If ToolConfigRef is specified, ensure it has a name
		if toolConfig.ToolConfigRef != nil && toolConfig.ToolConfigRef.Name == "" {
			return fmt.Errorf("spec.aggregation.tools[%d].toolConfigRef.name is required when toolConfigRef is specified", i)
		}
	}

	return nil
}

// validateCompositeTools validates composite tool definitions
func (r *VirtualMCPServer) validateCompositeTools() error {
	toolNames := make(map[string]bool)

	for i, tool := range r.Spec.CompositeTools {
		if err := r.validateCompositeTool(i, tool, toolNames); err != nil {
			return err
		}
	}

	return nil
}

// validateCompositeTool validates a single composite tool
func (*VirtualMCPServer) validateCompositeTool(index int, tool CompositeToolSpec, toolNames map[string]bool) error {
	// Check for required fields
	if tool.Name == "" {
		return fmt.Errorf("spec.compositeTools[%d].name is required", index)
	}
	if tool.Description == "" {
		return fmt.Errorf("spec.compositeTools[%d].description is required", index)
	}
	if len(tool.Steps) == 0 {
		return fmt.Errorf("spec.compositeTools[%d].steps must have at least one step", index)
	}

	// Check for duplicate tool names
	if toolNames[tool.Name] {
		return fmt.Errorf("spec.compositeTools[%d].name %q is duplicated", index, tool.Name)
	}
	toolNames[tool.Name] = true

	// Validate steps
	return validateCompositeToolSteps(index, tool.Steps)
}

// validateCompositeToolSteps validates all steps in a composite tool
func validateCompositeToolSteps(toolIndex int, steps []WorkflowStep) error {
	stepIDs := make(map[string]bool)

	for j, step := range steps {
		if err := validateCompositeToolStep(toolIndex, j, step, steps, stepIDs); err != nil {
			return err
		}
	}

	return nil
}

// validateCompositeToolStep validates a single workflow step
func validateCompositeToolStep(
	toolIndex, stepIndex int, step WorkflowStep, allSteps []WorkflowStep, stepIDs map[string]bool,
) error {
	if step.ID == "" {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].id is required", toolIndex, stepIndex)
	}

	// Check for duplicate step IDs
	if stepIDs[step.ID] {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].id %q is duplicated", toolIndex, stepIndex, step.ID)
	}
	stepIDs[step.ID] = true

	// Validate step type
	if err := validateStepType(toolIndex, stepIndex, step); err != nil {
		return err
	}

	// Validate dependsOn references
	if err := validateStepDependencies(toolIndex, stepIndex, step, allSteps, stepIDs); err != nil {
		return err
	}

	// Validate error handling
	return validateStepErrorHandling(toolIndex, stepIndex, step)
}

// validateStepType validates the step type and type-specific requirements
func validateStepType(toolIndex, stepIndex int, step WorkflowStep) error {
	if step.Type != "" && step.Type != WorkflowStepTypeToolCall && step.Type != WorkflowStepTypeElicitation {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].type must be tool or elicitation", toolIndex, stepIndex)
	}

	stepType := step.Type
	if stepType == "" {
		stepType = WorkflowStepTypeToolCall // default
	}

	if stepType == WorkflowStepTypeToolCall && step.Tool == "" {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].tool is required when type is tool", toolIndex, stepIndex)
	}

	if stepType == WorkflowStepTypeElicitation && step.Message == "" {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].message is required when type is elicitation", toolIndex, stepIndex)
	}

	return nil
}

// validateStepDependencies validates that dependsOn references exist
func validateStepDependencies(
	toolIndex, stepIndex int, step WorkflowStep, allSteps []WorkflowStep, stepIDs map[string]bool,
) error {
	for _, depID := range step.DependsOn {
		if !stepIDs[depID] {
			// Check if it's a forward reference
			found := false
			for k := stepIndex + 1; k < len(allSteps); k++ {
				if allSteps[k].ID == depID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("spec.compositeTools[%d].steps[%d].dependsOn references unknown step %q",
					toolIndex, stepIndex, depID)
			}
		}
	}
	return nil
}

// validateStepErrorHandling validates error handling configuration for a step
func validateStepErrorHandling(toolIndex, stepIndex int, step WorkflowStep) error {
	if step.OnError == nil || step.OnError.Action == "" {
		return nil
	}

	validActions := map[string]bool{
		ErrorActionAbort:    true,
		ErrorActionContinue: true,
		ErrorActionRetry:    true,
	}
	if !validActions[step.OnError.Action] {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].onError.action must be abort, continue, or retry",
			toolIndex, stepIndex)
	}

	if step.OnError.Action == ErrorActionRetry && step.OnError.MaxRetries < 1 {
		return fmt.Errorf("spec.compositeTools[%d].steps[%d].onError.maxRetries must be at least 1 when action is retry",
			toolIndex, stepIndex)
	}

	if step.OnError.Action == ErrorActionRetry && step.OnError.RetryDelay != "" {
		if err := validateDuration(step.OnError.RetryDelay); err != nil {
			return fmt.Errorf("spec.compositeTools[%d].steps[%d].onError.retryDelay: %w",
				toolIndex, stepIndex, err)
		}
	}

	return nil
}
