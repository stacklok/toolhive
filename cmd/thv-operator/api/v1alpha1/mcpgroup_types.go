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
	// Phase indicates current state
	// +optional
	// +kubebuilder:default=Pending
	Phase MCPGroupPhase `json:"phase,omitempty"`

	// Servers lists server names in this group
	// +optional
	Servers []string `json:"servers"`

	// ServerCount is the number of servers
	// +optional
	ServerCount int `json:"serverCount"`

	// Conditions represent observations
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
	ConditionReasonListMCPServersFailed    = "ListMCPServersFailed"
	ConditionReasonListMCPServersSucceeded = "ListMCPServersSucceeded"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printerColumn:name="Servers",type="integer",JSONPath=".status.serverCount",description="The number of MCPServers in this group"
//+kubebuilder:printerColumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of the MCPGroup"
//+kubebuilder:printerColumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of the MCPGroup"

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
