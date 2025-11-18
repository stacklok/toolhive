package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// VirtualMCPCompositeToolDefinitionSpec defines the desired state of VirtualMCPCompositeToolDefinition
type VirtualMCPCompositeToolDefinitionSpec struct {
	// Name is the workflow name exposed as a composite tool
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`
	Name string `json:"name"`

	// Description is a human-readable description of the workflow
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`

	// Parameters defines the input parameter schema for the workflow in JSON Schema format.
	// Should be a JSON Schema object with "type": "object" and "properties".
	// Per MCP specification, this should follow standard JSON Schema for tool inputSchema.
	// Example:
	//   {
	//     "type": "object",
	//     "properties": {
	//       "param1": {"type": "string", "default": "value"},
	//       "param2": {"type": "integer"}
	//     },
	//     "required": ["param2"]
	//   }
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`

	// Steps defines the workflow step definitions
	// Steps are executed sequentially in Phase 1
	// Phase 2 will support DAG execution via dependsOn
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Steps []WorkflowStep `json:"steps"`

	// Timeout is the overall workflow timeout
	// Defaults to 30m if not specified
	// +kubebuilder:default="30m"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// FailureMode defines the failure handling strategy
	// - abort: Stop execution on first failure (default)
	// - continue: Continue executing remaining steps
	// +kubebuilder:validation:Enum=abort;continue
	// +kubebuilder:default=abort
	// +optional
	FailureMode string `json:"failureMode,omitempty"`
}

// VirtualMCPCompositeToolDefinitionStatus defines the observed state of VirtualMCPCompositeToolDefinition
type VirtualMCPCompositeToolDefinitionStatus struct {
	// ValidationStatus indicates the validation state of the workflow
	// - Valid: Workflow structure is valid
	// - Invalid: Workflow has validation errors
	// +optional
	ValidationStatus ValidationStatus `json:"validationStatus,omitempty"`

	// ValidationErrors contains validation error messages if ValidationStatus is Invalid
	// +optional
	ValidationErrors []string `json:"validationErrors,omitempty"`

	// ReferencingVirtualServers lists VirtualMCPServer resources that reference this workflow
	// This helps track which servers need to be reconciled when this workflow changes
	// +optional
	ReferencingVirtualServers []string `json:"referencingVirtualServers,omitempty"`

	// ObservedGeneration is the most recent generation observed for this VirtualMCPCompositeToolDefinition
	// It corresponds to the resource's generation, which is updated on mutation by the API Server
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the workflow's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ValidationStatus represents the validation state of a workflow
// +kubebuilder:validation:Enum=Valid;Invalid;Unknown
type ValidationStatus string

const (
	// ValidationStatusValid indicates the workflow is valid
	ValidationStatusValid ValidationStatus = "Valid"

	// ValidationStatusInvalid indicates the workflow has validation errors
	ValidationStatusInvalid ValidationStatus = "Invalid"

	// ValidationStatusUnknown indicates validation hasn't been performed yet
	ValidationStatusUnknown ValidationStatus = "Unknown"
)

// Condition types for VirtualMCPCompositeToolDefinition
const (
	// ConditionTypeWorkflowValidated indicates whether the workflow has been validated
	ConditionTypeWorkflowValidated = "WorkflowValidated"

	// Note: ConditionTypeReady is shared across multiple resources and defined in mcpremoteproxy_types.go
)

// Condition reasons for VirtualMCPCompositeToolDefinition
const (
	// ConditionReasonValidationSuccess indicates workflow validation succeeded
	ConditionReasonValidationSuccess = "ValidationSuccess"

	// ConditionReasonValidationFailed indicates workflow validation failed
	ConditionReasonValidationFailed = "ValidationFailed"

	// ConditionReasonSchemaInvalid indicates parameter or step schema is invalid
	ConditionReasonSchemaInvalid = "SchemaInvalid"

	// ConditionReasonTemplateInvalid indicates template syntax is invalid
	ConditionReasonTemplateInvalid = "TemplateInvalid"

	// ConditionReasonDependencyCycle indicates step dependencies contain cycles
	ConditionReasonDependencyCycle = "DependencyCycle"

	// ConditionReasonToolNotFound indicates a referenced tool doesn't exist
	ConditionReasonToolNotFound = "ToolNotFound"

	// ConditionReasonWorkflowReady indicates the workflow is ready to use
	ConditionReasonWorkflowReady = "WorkflowReady"

	// ConditionReasonWorkflowNotReady indicates the workflow is not ready
	ConditionReasonWorkflowNotReady = "WorkflowNotReady"
)

// CompositeToolDefinitionRef references a VirtualMCPCompositeToolDefinition resource
type CompositeToolDefinitionRef struct {
	// Name is the name of the VirtualMCPCompositeToolDefinition resource in the same namespace
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// AdvancedWorkflowStep extends WorkflowStep with Phase 2 features
// This is embedded in WorkflowStep for future expansion
type AdvancedWorkflowStep struct {
	// RetryPolicy defines retry behavior for this step (Phase 2)
	// +optional
	RetryPolicy *RetryPolicy `json:"retryPolicy,omitempty"`

	// Transform defines output transformation template (Phase 2)
	// Allows mapping step output to different structure
	// +optional
	Transform string `json:"transform,omitempty"`

	// CacheKey defines a cache key template for result caching (Phase 2)
	// If specified and cache hit occurs, step is skipped
	// +optional
	CacheKey string `json:"cacheKey,omitempty"`
}

// RetryPolicy defines retry behavior for workflow steps
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=3
	// +optional
	MaxRetries int `json:"maxRetries,omitempty"`

	// BackoffStrategy defines the backoff strategy
	// - fixed: Fixed delay between retries
	// - exponential: Exponential backoff
	// +kubebuilder:validation:Enum=fixed;exponential
	// +kubebuilder:default=exponential
	// +optional
	BackoffStrategy string `json:"backoffStrategy,omitempty"`

	// InitialDelay is the initial delay before first retry
	// +kubebuilder:default="1s"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m))+$`
	// +optional
	InitialDelay string `json:"initialDelay,omitempty"`

	// MaxDelay is the maximum delay between retries
	// +kubebuilder:default="30s"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m))+$`
	// +optional
	MaxDelay string `json:"maxDelay,omitempty"`

	// RetryableErrors defines which errors should trigger retry
	// If empty, all errors are retryable
	// Supports regex patterns
	// +optional
	RetryableErrors []string `json:"retryableErrors,omitempty"`
}

// ElicitationStep defines user input elicitation (Phase 2)
type ElicitationStep struct {
	// Message is the elicitation message to display to the user
	// Supports template expansion
	// +kubebuilder:validation:Required
	Message string `json:"message"`

	// Schema defines the expected response schema
	// Uses JSON Schema format
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Schema *runtime.RawExtension `json:"schema,omitempty"`

	// Timeout is the maximum time to wait for user input
	// +kubebuilder:default="5m"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// DefaultResponse is the default response if user doesn't respond in time
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	DefaultResponse *runtime.RawExtension `json:"defaultResponse,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vmcpctd;compositetool
//+kubebuilder:printcolumn:name="Workflow",type="string",JSONPath=".spec.name",description="Workflow name"
//+kubebuilder:printcolumn:name="Steps",type="integer",JSONPath=".spec.steps[*]",description="Number of steps"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.validationStatus",description="Validation status"
//+kubebuilder:printcolumn:name="Refs",type="integer",JSONPath=".status.referencingVirtualServers[*]",description="Refs"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"

// VirtualMCPCompositeToolDefinition is the Schema for the virtualmcpcompositetooldefinitions API
// VirtualMCPCompositeToolDefinition defines reusable composite workflows that can be referenced
// by multiple VirtualMCPServer instances
type VirtualMCPCompositeToolDefinition struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMCPCompositeToolDefinitionSpec   `json:"spec,omitempty"`
	Status VirtualMCPCompositeToolDefinitionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMCPCompositeToolDefinitionList contains a list of VirtualMCPCompositeToolDefinition
type VirtualMCPCompositeToolDefinitionList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMCPCompositeToolDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualMCPCompositeToolDefinition{}, &VirtualMCPCompositeToolDefinitionList{})
}
