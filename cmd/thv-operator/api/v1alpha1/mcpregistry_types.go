package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// RegistrySourceTypeConfigMap is the type for registry data stored in ConfigMaps
	RegistrySourceTypeConfigMap = "configmap"

	// RegistrySourceTypeGit is the type for registry data stored in Git repositories
	RegistrySourceTypeGit = "git"
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

	// EnforceServers indicates whether MCPServers in this namespace must have their images
	// present in at least one registry in the namespace. When any registry in the namespace
	// has this field set to true, enforcement is enabled for the entire namespace.
	// MCPServers with images not found in any registry will be rejected.
	// When false (default), MCPServers can be deployed regardless of registry presence.
	// +kubebuilder:default=false
	// +optional
	EnforceServers bool `json:"enforceServers,omitempty"`
}

// MCPRegistrySource defines the source configuration for registry data
type MCPRegistrySource struct {
	// Type is the type of source (configmap, git)
	// +kubebuilder:validation:Enum=configmap;git
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

	// Git defines the Git repository source configuration
	// Only used when Type is "git"
	// +optional
	Git *GitSource `json:"git,omitempty"`
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

// GitSource defines Git repository source configuration
type GitSource struct {
	// Repository is the Git repository URL (HTTP/HTTPS/SSH)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^(file:///|https?://|git@|ssh://|git://).*"
	Repository string `json:"repository"`

	// Branch is the Git branch to use (mutually exclusive with Tag and Commit)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Branch string `json:"branch,omitempty"`

	// Tag is the Git tag to use (mutually exclusive with Branch and Commit)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Tag string `json:"tag,omitempty"`

	// Commit is the Git commit SHA to use (mutually exclusive with Branch and Tag)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Commit string `json:"commit,omitempty"`

	// Path is the path to the registry file within the repository
	// +kubebuilder:validation:Pattern=^.*\.json$
	// +kubebuilder:default=registry.json
	// +optional
	Path string `json:"path,omitempty"`
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
	// Phase represents the current overall phase of the MCPRegistry
	// Derived from sync and API status
	// +optional
	Phase MCPRegistryPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// SyncStatus provides detailed information about data synchronization
	// +optional
	SyncStatus *SyncStatus `json:"syncStatus,omitempty"`

	// APIStatus provides detailed information about the API service
	// +optional
	APIStatus *APIStatus `json:"apiStatus,omitempty"`

	// LastAppliedFilterHash is the hash of the last applied filter
	// +optional
	LastAppliedFilterHash string `json:"lastAppliedFilterHash,omitempty"`

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

// SyncStatus provides detailed information about data synchronization
type SyncStatus struct {
	// Phase represents the current synchronization phase
	// +kubebuilder:validation:Enum=Syncing;Complete;Failed
	Phase SyncPhase `json:"phase"`

	// Message provides additional information about the sync status
	// +optional
	Message string `json:"message,omitempty"`

	// LastAttempt is the timestamp of the last sync attempt
	// +optional
	LastAttempt *metav1.Time `json:"lastAttempt,omitempty"`

	// AttemptCount is the number of sync attempts since last success
	// +optional
	// +kubebuilder:validation:Minimum=0
	AttemptCount int `json:"attemptCount,omitempty"`

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
}

// APIStatus provides detailed information about the API service
type APIStatus struct {
	// Phase represents the current API service phase
	// +kubebuilder:validation:Enum=NotStarted;Deploying;Ready;Unhealthy;Error
	Phase APIPhase `json:"phase"`

	// Message provides additional information about the API status
	// +optional
	Message string `json:"message,omitempty"`

	// Endpoint is the URL where the API is accessible
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ReadySince is the timestamp when the API became ready
	// +optional
	ReadySince *metav1.Time `json:"readySince,omitempty"`
}

// SyncPhase represents the data synchronization state
// +kubebuilder:validation:Enum=Syncing;Complete;Failed
type SyncPhase string

const (
	// SyncPhaseSyncing means sync is currently in progress
	SyncPhaseSyncing SyncPhase = "Syncing"

	// SyncPhaseComplete means sync completed successfully
	SyncPhaseComplete SyncPhase = "Complete"

	// SyncPhaseFailed means sync failed
	SyncPhaseFailed SyncPhase = "Failed"
)

// APIPhase represents the API service state
// +kubebuilder:validation:Enum=NotStarted;Deploying;Ready;Unhealthy;Error
type APIPhase string

const (
	// APIPhaseNotStarted means API deployment has not been created
	APIPhaseNotStarted APIPhase = "NotStarted"

	// APIPhaseDeploying means API is being deployed
	APIPhaseDeploying APIPhase = "Deploying"

	// APIPhaseReady means API is ready to serve requests
	APIPhaseReady APIPhase = "Ready"

	// APIPhaseUnhealthy means API is deployed but not healthy
	APIPhaseUnhealthy APIPhase = "Unhealthy"

	// APIPhaseError means API deployment failed
	APIPhaseError APIPhase = "Error"
)

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
//+kubebuilder:printcolumn:name="Sync",type="string",JSONPath=".status.syncStatus.phase"
//+kubebuilder:printcolumn:name="API",type="string",JSONPath=".status.apiStatus.phase"
//+kubebuilder:printcolumn:name="Servers",type="integer",JSONPath=".status.syncStatus.serverCount"
//+kubebuilder:printcolumn:name="Last Sync",type="date",JSONPath=".status.syncStatus.lastSyncTime"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:scope=Namespaced,categories=toolhive
//nolint:lll
//+kubebuilder:validation:XValidation:rule="self.spec.source.type == 'configmap' ? has(self.spec.source.configmap) : (self.spec.source.type == 'git' ? has(self.spec.source.git) : true)",message="configMap field is required when source type is 'configmap', git field is required when source type is 'git'"

// MCPRegistry is the Schema for the mcpregistries API
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

// DeriveOverallPhase determines the overall MCPRegistry phase based on sync and API status
func (r *MCPRegistry) DeriveOverallPhase() MCPRegistryPhase {
	syncStatus := r.Status.SyncStatus
	apiStatus := r.Status.APIStatus

	// Default phases if status not set
	var syncPhase SyncPhase
	if syncStatus != nil {
		syncPhase = syncStatus.Phase
	}

	apiPhase := APIPhaseNotStarted
	if apiStatus != nil {
		apiPhase = apiStatus.Phase
	}

	// If sync failed, overall is Failed
	if syncPhase == SyncPhaseFailed {
		return MCPRegistryPhaseFailed
	}

	// If sync in progress, overall is Syncing
	if syncPhase == SyncPhaseSyncing {
		return MCPRegistryPhaseSyncing
	}

	// If sync is complete (no sync needed), check API status
	if syncPhase == SyncPhaseComplete {
		switch apiPhase {
		case APIPhaseReady:
			return MCPRegistryPhaseReady
		case APIPhaseError:
			return MCPRegistryPhaseFailed
		case APIPhaseNotStarted, APIPhaseDeploying, APIPhaseUnhealthy:
			return MCPRegistryPhasePending // API still starting/not healthy
		}
	}

	// Default to pending for initial states
	return MCPRegistryPhasePending
}

func init() {
	SchemeBuilder.Register(&MCPRegistry{}, &MCPRegistryList{})
}
