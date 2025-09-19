package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// RegistrySourceTypeConfigMap is the type for registry data stored in ConfigMaps
	RegistrySourceTypeConfigMap = "configmap"
)

// Registry formats
const (
	// RegistryFormatToolHive is the native ToolHive registry format
	RegistryFormatToolHive = "toolhive"

	// RegistryFormatUpstream is the upstream MCP registry format
	RegistryFormatUpstream = "upstream"
)

// MCPRegistrySpec defines the desired state of MCPRegistry
type MCPRegistrySpec struct {
	// DisplayName is a human-readable name for the registry
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Source defines the configuration for the registry data source
	// +kubebuilder:validation:Required
	Source MCPRegistrySource `json:"source"`

	// SyncPolicy defines the automatic synchronization behavior for the registry.
	// If specified, enables automatic synchronization at the given interval.
	// Manual synchronization is always supported via annotation-based triggers
	// regardless of this setting.
	// +optional
	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty"`

	// Filter defines include/exclude patterns for registry content
	// +optional
	Filter *RegistryFilter `json:"filter,omitempty"`
}

// MCPRegistrySource defines the source configuration for registry data
type MCPRegistrySource struct {
	// Type is the type of source (configmap)
	// +kubebuilder:validation:Enum=configmap
	// +kubebuilder:default=configmap
	Type string `json:"type"`

	// Format is the data format (toolhive, upstream)
	// +kubebuilder:validation:Enum=toolhive;upstream
	// +kubebuilder:default=toolhive
	Format string `json:"format,omitempty"`

	// ConfigMap defines the ConfigMap source configuration
	// Only used when Type is "configmap"
	// +optional
	ConfigMap *ConfigMapSource `json:"configmap,omitempty"`
}

// ConfigMapSource defines ConfigMap source configuration
type ConfigMapSource struct {
	// Name is the name of the ConfigMap
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key is the key in the ConfigMap that contains the registry data
	// +kubebuilder:default=registry.json
	// +kubebuilder:validation:MinLength=1
	// +optional
	Key string `json:"key,omitempty"`
}

// SyncPolicy defines automatic synchronization behavior.
// When specified, enables automatic synchronization at the given interval.
// Manual synchronization via annotation-based triggers is always available
// regardless of this policy setting.
type SyncPolicy struct {
	// Interval is the sync interval for automatic synchronization (Go duration format)
	// Examples: "1h", "30m", "24h"
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	// +kubebuilder:validation:Required
	Interval string `json:"interval"`
}

// RegistryFilter defines include/exclude patterns for registry content
type RegistryFilter struct {
	// NameFilters defines name-based filtering
	// +optional
	NameFilters *NameFilter `json:"names,omitempty"`

	// Tags defines tag-based filtering
	// +optional
	Tags *TagFilter `json:"tags,omitempty"`
}

// NameFilter defines name-based filtering
type NameFilter struct {
	// Include is a list of glob patterns to include
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of glob patterns to exclude
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// TagFilter defines tag-based filtering
type TagFilter struct {
	// Include is a list of tags to include
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of tags to exclude
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// MCPRegistryStatus defines the observed state of MCPRegistry
type MCPRegistryStatus struct {
	// Phase represents the current phase of the MCPRegistry
	// +optional
	Phase MCPRegistryPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// LastSyncTime is the timestamp of the last successful sync
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// LastSyncHash is the hash of the last successfully synced data
	// Used to detect changes in source data
	// +optional
	LastSyncHash string `json:"lastSyncHash,omitempty"`

	// ServerCount is the total number of servers in the registry
	// +optional
	// +kubebuilder:validation:Minimum=0
	ServerCount int `json:"serverCount,omitempty"`

	// DeployedServerCount is the number of deployed servers with matching labels
	// +optional
	// +kubebuilder:validation:Minimum=0
	DeployedServerCount int `json:"deployedServerCount,omitempty"`

	// SyncAttempts is the number of sync attempts since last success
	// +optional
	// +kubebuilder:validation:Minimum=0
	SyncAttempts int `json:"syncAttempts,omitempty"`

	// APIEndpoint is the URL of the registry API service
	// +optional
	APIEndpoint string `json:"apiEndpoint,omitempty"`

	// StorageRef is a reference to the internal storage location
	// +optional
	StorageRef *StorageReference `json:"storageRef,omitempty"`

	// LastManualSyncTrigger tracks the last processed manual sync annotation value
	// Used to detect new manual sync requests via toolhive.stacklok.dev/sync-trigger annotation
	// +optional
	LastManualSyncTrigger string `json:"lastManualSyncTrigger,omitempty"`

	// Conditions represent the latest available observations of the MCPRegistry's state
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StorageReference defines a reference to internal storage
type StorageReference struct {
	// Type is the storage type (configmap)
	// +kubebuilder:validation:Enum=configmap
	Type string `json:"type"`

	// ConfigMapRef is a reference to a ConfigMap storage
	// Only used when Type is "configmap"
	// +optional
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`
}

// MCPRegistryPhase represents the phase of the MCPRegistry
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Syncing;Terminating
type MCPRegistryPhase string

const (
	// MCPRegistryPhasePending means the MCPRegistry is being initialized
	MCPRegistryPhasePending MCPRegistryPhase = "Pending"

	// MCPRegistryPhaseReady means the MCPRegistry is ready and operational
	MCPRegistryPhaseReady MCPRegistryPhase = "Ready"

	// MCPRegistryPhaseFailed means the MCPRegistry has failed
	MCPRegistryPhaseFailed MCPRegistryPhase = "Failed"

	// MCPRegistryPhaseSyncing means the MCPRegistry is currently syncing data
	MCPRegistryPhaseSyncing MCPRegistryPhase = "Syncing"

	// MCPRegistryPhaseTerminating means the MCPRegistry is being deleted
	MCPRegistryPhaseTerminating MCPRegistryPhase = "Terminating"
)

// Condition types for MCPRegistry
const (
	// ConditionSourceAvailable indicates whether the source is available and accessible
	ConditionSourceAvailable = "SourceAvailable"

	// ConditionDataValid indicates whether the registry data is valid
	ConditionDataValid = "DataValid"

	// ConditionSyncSuccessful indicates whether the last sync was successful
	ConditionSyncSuccessful = "SyncSuccessful"

	// ConditionAPIReady indicates whether the registry API is ready
	ConditionAPIReady = "APIReady"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Servers",type="integer",JSONPath=".status.serverCount"
//+kubebuilder:printcolumn:name="Deployed",type="integer",JSONPath=".status.deployedServerCount"
//+kubebuilder:printcolumn:name="Last Sync",type="date",JSONPath=".status.lastSyncTime"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:scope=Namespaced,categories=toolhive
//nolint:lll
//+kubebuilder:validation:XValidation:rule="self.spec.source.type == 'configmap' ? has(self.spec.source.configmap) : true",message="configMap field is required when source type is 'configmap'"

// MCPRegistry is the Schema for the mcpregistries API
// ⚠️ Experimental API (v1alpha1) — subject to change.
type MCPRegistry struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPRegistrySpec   `json:"spec,omitempty"`
	Status MCPRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPRegistryList contains a list of MCPRegistry
type MCPRegistryList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRegistry `json:"items"`
}

// GetStorageName returns the name used for registry storage resources
func (r *MCPRegistry) GetStorageName() string {
	return fmt.Sprintf("%s-registry-storage", r.Name)
}

// GetAPIResourceName returns the base name for registry API resources (deployment, service)
func (r *MCPRegistry) GetAPIResourceName() string {
	return fmt.Sprintf("%s-api", r.Name)
}

func init() {
	SchemeBuilder.Register(&MCPRegistry{}, &MCPRegistryList{})
}
