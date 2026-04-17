// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPServerEntrySpec defines the desired state of MCPServerEntry.
// MCPServerEntry is a zero-infrastructure catalog entry that declares a remote MCP
// server endpoint. Unlike MCPRemoteProxy, it creates no pods, services, or deployments.
type MCPServerEntrySpec struct {
	// RemoteURL is the URL of the remote MCP server.
	// Both HTTP and HTTPS schemes are accepted at admission time.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://`
	RemoteURL string `json:"remoteUrl"`

	// Transport is the transport method for the remote server (sse or streamable-http).
	// No default is set (unlike MCPRemoteProxy) because MCPServerEntry points at external
	// servers the user doesn't control — requiring explicit transport avoids silent mismatches.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=sse;streamable-http
	Transport string `json:"transport"`

	// GroupRef references the MCPGroup this entry belongs to.
	// Required — every MCPServerEntry must be part of a group for vMCP discovery.
	// +kubebuilder:validation:Required
	GroupRef *MCPGroupRef `json:"groupRef"`

	// ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange
	// when connecting to the remote MCP server. The referenced MCPExternalAuthConfig must
	// exist in the same namespace as this MCPServerEntry.
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`

	// HeaderForward configures headers to inject into requests to the remote MCP server.
	// Use this to add custom headers like API keys or correlation IDs.
	// +optional
	HeaderForward *HeaderForwardConfig `json:"headerForward,omitempty"`

	// CABundleRef references a ConfigMap containing CA certificates for TLS verification
	// when connecting to the remote MCP server.
	// +optional
	CABundleRef *CABundleSource `json:"caBundleRef,omitempty"`
}

// MCPServerEntryStatus defines the observed state of MCPServerEntry.
type MCPServerEntryStatus struct {
	// ObservedGeneration reflects the generation most recently observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase indicates the current lifecycle phase of the MCPServerEntry.
	// +optional
	// +kubebuilder:default=Pending
	Phase MCPServerEntryPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations of the MCPServerEntry's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MCPServerEntryPhase represents the lifecycle phase of an MCPServerEntry.
// +kubebuilder:validation:Enum=Valid;Pending;Failed
type MCPServerEntryPhase string

const (
	// MCPServerEntryPhaseValid indicates all validations passed and the entry is usable.
	MCPServerEntryPhaseValid MCPServerEntryPhase = "Valid"

	// MCPServerEntryPhasePending is the initial state before the first reconciliation.
	MCPServerEntryPhasePending MCPServerEntryPhase = "Pending"

	// MCPServerEntryPhaseFailed indicates one or more referenced resources are missing or invalid.
	MCPServerEntryPhaseFailed MCPServerEntryPhase = "Failed"
)

// Condition types for MCPServerEntry.
// Reuses shared condition type constants from mcpserver_types.go where the string
// values match (GroupRefValidated, ExternalAuthConfigValidated, CABundleRefValidated).
const (
	// ConditionTypeMCPServerEntryValid indicates overall validation status of the MCPServerEntry.
	// Uses the shared "Valid" condition type since this is a configuration resource, not a workload.
	ConditionTypeMCPServerEntryValid = ConditionTypeValid

	// ConditionTypeMCPServerEntryGroupRefValidated indicates whether the referenced MCPGroup exists.
	ConditionTypeMCPServerEntryGroupRefValidated = ConditionGroupRefValidated

	// ConditionTypeMCPServerEntryAuthConfigValidated indicates whether the referenced
	// MCPExternalAuthConfig exists (when configured).
	ConditionTypeMCPServerEntryAuthConfigValidated = ConditionTypeExternalAuthConfigValidated

	// ConditionTypeMCPServerEntryCABundleRefValidated indicates whether the referenced
	// CA bundle ConfigMap exists (when configured).
	ConditionTypeMCPServerEntryCABundleRefValidated = ConditionCABundleRefValidated

	// ConditionTypeMCPServerEntryRemoteURLValidated indicates whether the RemoteURL passes
	// format and SSRF safety checks.
	ConditionTypeMCPServerEntryRemoteURLValidated = "RemoteURLValidated"
)

// Condition reasons for MCPServerEntry.
// GroupRef reasons reuse shared constants from mcpserver_types.go.
// CABundle reasons reuse shared constants from mcpserver_types.go.
const (
	// ConditionReasonMCPServerEntryValid indicates the entry passed all validations.
	ConditionReasonMCPServerEntryValid = "ConfigValid"

	// ConditionReasonMCPServerEntryInvalid indicates one or more validations failed.
	ConditionReasonMCPServerEntryInvalid = "ConfigInvalid"

	// ConditionReasonMCPServerEntryGroupRefValidated reuses the shared GroupRef reason.
	ConditionReasonMCPServerEntryGroupRefValidated = ConditionReasonGroupRefValidated

	// ConditionReasonMCPServerEntryGroupRefNotFound reuses the shared GroupRef reason.
	ConditionReasonMCPServerEntryGroupRefNotFound = ConditionReasonGroupRefNotFound

	// ConditionReasonMCPServerEntryGroupRefNotReady reuses the shared GroupRef reason.
	ConditionReasonMCPServerEntryGroupRefNotReady = ConditionReasonGroupRefNotReady

	// ConditionReasonMCPServerEntryAuthConfigValid indicates the referenced auth config exists.
	ConditionReasonMCPServerEntryAuthConfigValid = "AuthConfigValid"

	// ConditionReasonMCPServerEntryAuthConfigNotFound indicates the referenced auth config was not found.
	ConditionReasonMCPServerEntryAuthConfigNotFound = "AuthConfigNotFound"

	// ConditionReasonMCPServerEntryAuthConfigNotConfigured indicates no auth config ref is set.
	ConditionReasonMCPServerEntryAuthConfigNotConfigured = "AuthConfigNotConfigured"

	// ConditionReasonMCPServerEntryCABundleRefValid reuses the shared CABundle reason.
	ConditionReasonMCPServerEntryCABundleRefValid = ConditionReasonCABundleRefValid

	// ConditionReasonMCPServerEntryCABundleRefNotFound reuses the shared CABundle reason.
	ConditionReasonMCPServerEntryCABundleRefNotFound = ConditionReasonCABundleRefNotFound

	// ConditionReasonMCPServerEntryCABundleRefNotConfigured indicates no CA bundle ref is set.
	ConditionReasonMCPServerEntryCABundleRefNotConfigured = "CABundleRefNotConfigured"

	// ConditionReasonMCPServerEntryRemoteURLValid indicates the RemoteURL passed all checks.
	ConditionReasonMCPServerEntryRemoteURLValid = "RemoteURLValid"

	// ConditionReasonMCPServerEntryRemoteURLInvalid indicates the RemoteURL is malformed or
	// targets a blocked internal/metadata endpoint.
	ConditionReasonMCPServerEntryRemoteURLInvalid = ConditionReasonRemoteURLInvalid
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpentry,categories=toolhive
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Transport",type="string",JSONPath=".spec.transport"
//+kubebuilder:printcolumn:name="Remote URL",type="string",JSONPath=".spec.remoteUrl"
//+kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.groupRef.name"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServerEntry is the Schema for the mcpserverentries API.
// It declares a remote MCP server endpoint for vMCP discovery and routing
// without deploying any infrastructure.
type MCPServerEntry struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerEntrySpec   `json:"spec,omitempty"`
	Status MCPServerEntryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPServerEntryList contains a list of MCPServerEntry.
type MCPServerEntryList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServerEntry `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServerEntry{}, &MCPServerEntryList{})
}
