package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Condition types for EmbeddingServer (reuses common conditions from MCPServer)
// ConditionImageValidated and ConditionPodTemplateValid are shared with MCPServer

const (
	// ConditionModelReady indicates whether the embedding model is downloaded and ready
	ConditionModelReady = "ModelReady"

	// ConditionVolumeReady indicates whether the PVC for model caching is ready
	ConditionVolumeReady = "VolumeReady"
)

// Condition reasons for EmbeddingServer
// Image validation and PodTemplate reasons are shared with MCPServer

const (
	// ConditionReasonModelDownloading indicates the model is being downloaded
	ConditionReasonModelDownloading = "ModelDownloading"
	// ConditionReasonModelReady indicates the model is downloaded and ready
	ConditionReasonModelReady = "ModelReady"
	// ConditionReasonModelFailed indicates the model download or initialization failed
	ConditionReasonModelFailed = "ModelFailed"

	// ConditionReasonVolumeCreating indicates the PVC is being created
	ConditionReasonVolumeCreating = "VolumeCreating"
	// ConditionReasonVolumeReady indicates the PVC is ready
	ConditionReasonVolumeReady = "VolumeReady"
	// ConditionReasonVolumeFailed indicates the PVC creation failed
	ConditionReasonVolumeFailed = "VolumeFailed"
)

// EmbeddingServerSpec defines the desired state of EmbeddingServer
type EmbeddingServerSpec struct {
	// Model is the HuggingFace embedding model to use (e.g., "sentence-transformers/all-MiniLM-L6-v2")
	// +kubebuilder:validation:Required
	Model string `json:"model"`

	// HFTokenSecretRef is a reference to a Kubernetes Secret containing the huggingface token.
	// If provided, the secret value will be provided to the embedding server for authentication with huggingface.
	// +optional
	HFTokenSecretRef *SecretKeyRef `json:"hfTokenSecretRef,omitempty"`

	// Image is the container image for huggingface-embedding-inference
	// +kubebuilder:validation:Required
	// +kubebuilder:default="ghcr.io/huggingface/text-embeddings-inference:latest"
	Image string `json:"image,omitempty"`

	// ImagePullPolicy defines the pull policy for the container image
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	// +kubebuilder:default="IfNotPresent"
	// +optional
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"`

	// Port is the port to expose the embedding service on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`

	// Args are additional arguments to pass to the embedding inference server
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are environment variables to set in the container
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Resources defines compute resources for the embedding server
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// ModelCache configures persistent storage for downloaded models
	// When enabled, models are cached in a PVC and reused across pod restarts
	// +optional
	ModelCache *ModelCacheConfig `json:"modelCache,omitempty"`

	// PodTemplateSpec allows customizing the pod (node selection, tolerations, etc.)
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// Note that to modify the specific container the embedding server runs in, you must specify
	// the 'embedding' container name in the PodTemplateSpec.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// ResourceOverrides allows overriding annotations and labels for resources created by the operator
	// +optional
	ResourceOverrides *EmbeddingResourceOverrides `json:"resourceOverrides,omitempty"`

	// Replicas is the number of embedding server replicas to run
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
}

// ModelCacheConfig configures persistent storage for model caching
type ModelCacheConfig struct {
	// Enabled controls whether model caching is enabled
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// StorageClassName is the storage class to use for the PVC
	// If not specified, uses the cluster's default storage class
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Size is the size of the PVC for model caching (e.g., "10Gi")
	// +kubebuilder:default="10Gi"
	// +optional
	Size string `json:"size,omitempty"`

	// AccessMode is the access mode for the PVC
	// +kubebuilder:default="ReadWriteOnce"
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteMany;ReadOnlyMany
	// +optional
	AccessMode string `json:"accessMode,omitempty"`
}

// EmbeddingResourceOverrides defines overrides for annotations and labels on created resources
type EmbeddingResourceOverrides struct {
	// Deployment defines overrides for the Deployment resource
	// +optional
	Deployment *EmbeddingDeploymentOverrides `json:"deployment,omitempty"`

	// Service defines overrides for the Service resource
	// +optional
	Service *ResourceMetadataOverrides `json:"service,omitempty"`

	// PersistentVolumeClaim defines overrides for the PVC resource
	// +optional
	PersistentVolumeClaim *ResourceMetadataOverrides `json:"persistentVolumeClaim,omitempty"`
}

// EmbeddingDeploymentOverrides defines overrides specific to the embedding deployment
type EmbeddingDeploymentOverrides struct {
	// ResourceMetadataOverrides is embedded to inherit annotations and labels fields
	ResourceMetadataOverrides `json:",inline"` // nolint:revive

	// PodTemplateMetadataOverrides defines metadata overrides for the pod template
	// +optional
	PodTemplateMetadataOverrides *ResourceMetadataOverrides `json:"podTemplateMetadataOverrides,omitempty"`
}

// EmbeddingServerStatus defines the observed state of EmbeddingServer
type EmbeddingServerStatus struct {
	// Conditions represent the latest available observations of the EmbeddingServer's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is the current phase of the EmbeddingServer
	// +optional
	Phase EmbeddingServerPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// URL is the URL where the embedding service can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// ReadyReplicas is the number of ready replicas
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// EmbeddingServerPhase is the phase of the EmbeddingServer
// +kubebuilder:validation:Enum=Pending;Downloading;Running;Failed;Terminating
type EmbeddingServerPhase string

const (
	// EmbeddingServerPhasePending means the EmbeddingServer is being created
	EmbeddingServerPhasePending EmbeddingServerPhase = "Pending"

	// EmbeddingServerPhaseDownloading means the model is being downloaded
	EmbeddingServerPhaseDownloading EmbeddingServerPhase = "Downloading"

	// EmbeddingServerPhaseRunning means the EmbeddingServer is running and ready
	EmbeddingServerPhaseRunning EmbeddingServerPhase = "Running"

	// EmbeddingServerPhaseFailed means the EmbeddingServer failed to start
	EmbeddingServerPhaseFailed EmbeddingServerPhase = "Failed"

	// EmbeddingServerPhaseTerminating means the EmbeddingServer is being deleted
	EmbeddingServerPhaseTerminating EmbeddingServerPhase = "Terminating"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Model",type="string",JSONPath=".spec.model"
//+kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EmbeddingServer is the Schema for the embeddingservers API
type EmbeddingServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EmbeddingServerSpec   `json:"spec,omitempty"`
	Status EmbeddingServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EmbeddingServerList contains a list of EmbeddingServer
type EmbeddingServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EmbeddingServer `json:"items"`
}

// GetName returns the name of the EmbeddingServer
func (e *EmbeddingServer) GetName() string {
	return e.Name
}

// GetNamespace returns the namespace of the EmbeddingServer
func (e *EmbeddingServer) GetNamespace() string {
	return e.Namespace
}

// GetPort returns the port of the EmbeddingServer
func (e *EmbeddingServer) GetPort() int32 {
	if e.Spec.Port > 0 {
		return e.Spec.Port
	}
	return 8080
}

// GetReplicas returns the number of replicas for the EmbeddingServer
func (e *EmbeddingServer) GetReplicas() int32 {
	if e.Spec.Replicas != nil {
		return *e.Spec.Replicas
	}
	return 1
}

// IsModelCacheEnabled returns whether model caching is enabled
func (e *EmbeddingServer) IsModelCacheEnabled() bool {
	if e.Spec.ModelCache == nil {
		return false
	}
	return e.Spec.ModelCache.Enabled
}

// GetImagePullPolicy returns the image pull policy for the EmbeddingServer
func (e *EmbeddingServer) GetImagePullPolicy() string {
	if e.Spec.ImagePullPolicy != "" {
		return e.Spec.ImagePullPolicy
	}
	return "IfNotPresent"
}

func init() {
	SchemeBuilder.Register(&EmbeddingServer{}, &EmbeddingServerList{})
}
