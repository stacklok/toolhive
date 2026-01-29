// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// VirtualMCPCompositeToolDefinitionSpec defines the desired state of VirtualMCPCompositeToolDefinition.
// This embeds the CompositeToolConfig from pkg/vmcp/config to share the configuration model
// between CLI and operator usage.
type VirtualMCPCompositeToolDefinitionSpec struct {
	config.CompositeToolConfig `json:",inline"` // nolint:revive // inline is valid
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
