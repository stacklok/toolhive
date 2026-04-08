// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPGroupSpec defines the desired state of MCPGroup
type MCPGroupSpec struct {
	// Description provides human-readable context
	// +optional
	Description string `json:"description,omitempty"`
}

// MCPGroupStatus defines observed state
type MCPGroupStatus struct {
	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase indicates current state
	// +optional
	// +kubebuilder:default=Pending
	Phase MCPGroupPhase `json:"phase,omitempty"`

	// Servers lists MCPServer names in this group
	// +listType=set
	// +optional
	Servers []string `json:"servers"`

	// ServerCount is the number of MCPServers
	// +optional
	ServerCount int32 `json:"serverCount"`

	// RemoteProxies lists MCPRemoteProxy names in this group
	// +listType=set
	// +optional
	RemoteProxies []string `json:"remoteProxies,omitempty"`

	// RemoteProxyCount is the number of MCPRemoteProxies
	// +optional
	RemoteProxyCount int32 `json:"remoteProxyCount,omitempty"`

	// Entries lists MCPServerEntry names in this group
	// +listType=set
	// +optional
	Entries []string `json:"entries,omitempty"`

	// EntryCount is the number of MCPServerEntries
	// +optional
	EntryCount int32 `json:"entryCount,omitempty"`

	// Conditions represent observations
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MCPGroupPhase represents the lifecycle phase of an MCPGroup
// +kubebuilder:validation:Enum=Ready;Pending;Failed
type MCPGroupPhase string

const (
	// MCPGroupPhaseReady indicates the MCPGroup is ready
	MCPGroupPhaseReady MCPGroupPhase = "Ready"

	// MCPGroupPhasePending indicates the MCPGroup is pending
	MCPGroupPhasePending MCPGroupPhase = "Pending"

	// MCPGroupPhaseFailed indicates the MCPGroup has failed
	MCPGroupPhaseFailed MCPGroupPhase = "Failed"
)

// Condition types for MCPGroup
const (
	ConditionTypeMCPServersChecked = "MCPServersChecked"
)

// MCPGroupConditionReason represents the reason for a condition's last transition
const (
	ConditionReasonListMCPServersFailed    = "ListMCPServersCheckFailed"
	ConditionReasonListMCPServersSucceeded = "ListMCPServersCheckSucceeded"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpg;mcpgroup,categories=toolhive
//+kubebuilder:printcolumn:name="Servers",type="integer",JSONPath=".status.serverCount"
//+kubebuilder:printcolumn:name="Entries",type="integer",JSONPath=".status.entryCount"
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='MCPServersChecked')].status"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPGroup is the Schema for the mcpgroups API
type MCPGroup struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPGroupSpec   `json:"spec,omitempty"`
	Status MCPGroupStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPGroupList contains a list of MCPGroup
type MCPGroupList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPGroup{}, &MCPGroupList{})
}
