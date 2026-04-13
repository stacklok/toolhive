// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// Condition type and reasons for MCPAuthzConfig status (RFC-0023)
const (
	// ConditionTypeAuthzConfigValid indicates whether the MCPAuthzConfig configuration is valid
	ConditionTypeAuthzConfigValid = ConditionTypeValid

	// ConditionReasonAuthzConfigValid indicates spec validation passed
	ConditionReasonAuthzConfigValid = "ConfigValid"

	// ConditionReasonAuthzConfigInvalid indicates spec validation failed
	ConditionReasonAuthzConfigInvalid = "ConfigInvalid"
)

// MCPAuthzConfigSpec defines the desired state of MCPAuthzConfig.
// MCPAuthzConfig resources are namespace-scoped and can only be referenced by
// MCPServer, MCPRemoteProxy, or VirtualMCPServer resources in the same namespace.
type MCPAuthzConfigSpec struct {
	// Type identifies the authorizer backend (e.g., "cedarv1", "httpv1").
	// Must match a registered authorizer type in the factory registry.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Config contains the backend-specific authorization configuration.
	// The structure depends on the Type field:
	//   - cedarv1: policies ([]string), entities_json (string), primary_upstream_provider (string), group_claim_name (string)
	//   - httpv1: http ({url, timeout, insecure_skip_verify}), context ({include_args, include_operation}), claim_mapping (string)
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Config runtime.RawExtension `json:"config"`
}

// MCPAuthzConfigStatus defines the observed state of MCPAuthzConfig
type MCPAuthzConfigStatus struct {
	// Conditions represent the latest available observations of the MCPAuthzConfig's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPAuthzConfig.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingWorkloads is a list of workload resources that reference this MCPAuthzConfig.
	// Each entry identifies the workload by kind and name.
	// +listType=map
	// +listMapKey=name
	// +optional
	ReferencingWorkloads []WorkloadReference `json:"referencingWorkloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=authzcfg,categories=toolhive
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPAuthzConfig is the Schema for the mcpauthzconfigs API.
// MCPAuthzConfig resources are namespace-scoped and can only be referenced by
// MCPServer, MCPRemoteProxy, or VirtualMCPServer resources within the same namespace.
// Cross-namespace references are not supported for security and isolation reasons.
type MCPAuthzConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPAuthzConfigSpec   `json:"spec,omitempty"`
	Status MCPAuthzConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPAuthzConfigList contains a list of MCPAuthzConfig
type MCPAuthzConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPAuthzConfig `json:"items"`
}

// MCPAuthzConfigReference references a shared MCPAuthzConfig resource.
// The referenced MCPAuthzConfig must be in the same namespace as the referencing workload.
type MCPAuthzConfigReference struct {
	// Name is the name of the MCPAuthzConfig resource in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&MCPAuthzConfig{}, &MCPAuthzConfigList{})
}
