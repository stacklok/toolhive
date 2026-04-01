// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types for MCPToolConfig
const (
	// ConditionToolConfigValid indicates whether the MCPToolConfig spec is valid.
	ConditionToolConfigValid = "Valid"
)

const (
	// ConditionReasonToolConfigValidationSucceeded indicates validation passed.
	ConditionReasonToolConfigValidationSucceeded = "ValidationSucceeded"
	// ConditionReasonToolConfigValidationFailed indicates validation failed.
	ConditionReasonToolConfigValidationFailed = "ValidationFailed"
)

// MCPToolConfigSpec defines the desired state of MCPToolConfig.
// MCPToolConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources in the same namespace.
type MCPToolConfigSpec struct {
	// ToolsFilter is a list of tool names to filter (allow list).
	// Only tools in this list will be exposed by the MCP server.
	// If empty, all tools are exposed.
	// +optional
	ToolsFilter []string `json:"toolsFilter,omitempty"`

	// ToolsOverride is a map from actual tool names to their overridden configuration.
	// This allows renaming tools and/or changing their descriptions.
	// +optional
	ToolsOverride map[string]ToolOverride `json:"toolsOverride,omitempty"`
}

// ToolAnnotationsOverride defines overrides for tool annotation fields.
// All fields use pointers so nil means "don't override" while zero values
// (empty string, false) mean "explicitly set to this value."
type ToolAnnotationsOverride struct {
	// Title overrides the human-readable title annotation.
	// +optional
	Title *string `json:"title,omitempty"`

	// ReadOnlyHint overrides the read-only hint annotation.
	// +optional
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`

	// DestructiveHint overrides the destructive hint annotation.
	// +optional
	DestructiveHint *bool `json:"destructiveHint,omitempty"`

	// IdempotentHint overrides the idempotent hint annotation.
	// +optional
	IdempotentHint *bool `json:"idempotentHint,omitempty"`

	// OpenWorldHint overrides the open-world hint annotation.
	// +optional
	OpenWorldHint *bool `json:"openWorldHint,omitempty"`
}

// ToolOverride represents a tool override configuration.
// Both Name and Description can be overridden independently, but
// they can't be both empty.
type ToolOverride struct {
	// Name is the redefined name of the tool
	// +optional
	Name string `json:"name,omitempty"`

	// Description is the redefined description of the tool
	// +optional
	Description string `json:"description,omitempty"`

	// Annotations overrides specific tool annotation fields.
	// Only specified fields are overridden; others pass through from the backend.
	// +optional
	Annotations *ToolAnnotationsOverride `json:"annotations,omitempty"`
}

// MCPToolConfigStatus defines the observed state of MCPToolConfig
type MCPToolConfigStatus struct {
	// Conditions represent the latest available observations of the MCPToolConfig's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPToolConfig.
	// It corresponds to the MCPToolConfig's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingServers is a list of MCPServer resources that reference this MCPToolConfig
	// This helps track which servers need to be reconciled when this config changes
	// +optional
	ReferencingServers []string `json:"referencingServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tc;toolconfig,categories=toolhive
// +kubebuilder:printcolumn:name="Filter Count",type=integer,JSONPath=`.spec.toolsFilter[*]`
// +kubebuilder:printcolumn:name="Override Count",type=integer,JSONPath=`.spec.toolsOverride`
// +kubebuilder:printcolumn:name="Referenced By",type=string,JSONPath=`.status.referencingServers`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPToolConfig is the Schema for the mcptoolconfigs API.
// MCPToolConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources within the same namespace. Cross-namespace references
// are not supported for security and isolation reasons.
type MCPToolConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPToolConfigSpec   `json:"spec,omitempty"`
	Status MCPToolConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPToolConfigList contains a list of MCPToolConfig
type MCPToolConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPToolConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPToolConfig{}, &MCPToolConfigList{})
}
