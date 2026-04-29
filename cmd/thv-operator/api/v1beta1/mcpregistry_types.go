// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MCPRegistrySpec defines the desired state of MCPRegistry
type MCPRegistrySpec struct {
	// ConfigYAML is the complete registry server config.yaml content.
	// The operator creates a ConfigMap from this string and mounts it
	// at /config/config.yaml in the registry-api container.
	// The operator does NOT parse, validate, or transform this content —
	// configuration validation is the registry server's responsibility.
	//
	// Security note: this content is stored in a ConfigMap, not a Secret.
	// Do not inline credentials (passwords, tokens, client secrets) in this
	// field. Instead, reference credentials via file paths and mount the
	// actual secrets using the Volumes and VolumeMounts fields. For database
	// passwords, use PGPassSecretRef.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ConfigYAML string `json:"configYAML"`

	// Volumes defines additional volumes to add to the registry API pod.
	// Each entry is a standard Kubernetes Volume object (JSON/YAML).
	// The operator appends them to the pod spec alongside its own config volume.
	//
	// Use these to mount:
	//   - Secrets (git auth tokens, OAuth client secrets, CA certs)
	//   - ConfigMaps (registry data files)
	//   - PersistentVolumeClaims (registry data on persistent storage)
	//   - Any other volume type the registry server needs
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:pruning:PreserveUnknownFields
	Volumes []apiextensionsv1.JSON `json:"volumes,omitempty"`

	// VolumeMounts defines additional volume mounts for the registry-api container.
	// Each entry is a standard Kubernetes VolumeMount object (JSON/YAML).
	// The operator appends them to the container's volume mounts alongside the config mount.
	//
	// Mount paths must match the file paths referenced in configYAML.
	// For example, if configYAML references passwordFile: /secrets/git-creds/token,
	// a corresponding volume mount must exist with mountPath: /secrets/git-creds.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:pruning:PreserveUnknownFields
	VolumeMounts []apiextensionsv1.JSON `json:"volumeMounts,omitempty"`

	// PGPassSecretRef references a Secret containing a pre-created pgpass file.
	//
	// Why this is a dedicated field instead of a regular volume/volumeMount:
	// PostgreSQL's libpq rejects pgpass files that aren't mode 0600. Kubernetes
	// secret volumes mount files as root-owned, and the registry-api container
	// runs as non-root (UID 65532). A root-owned 0600 file is unreadable by
	// UID 65532, and using fsGroup changes permissions to 0640 which libpq also
	// rejects. The only solution is an init container that copies the file to an
	// emptyDir as the app user and runs chmod 0600. This cannot be expressed
	// through volumes/volumeMounts alone -- it requires an init container, two
	// extra volumes (secret + emptyDir), a subPath mount, and an environment
	// variable, all wired together correctly.
	//
	// When specified, the operator generates all of that plumbing invisibly.
	// The user creates the Secret with pgpass-formatted content; the operator
	// handles only the Kubernetes permission mechanics.
	//
	// Example Secret:
	//
	//	apiVersion: v1
	//	kind: Secret
	//	metadata:
	//	  name: my-pgpass
	//	stringData:
	//	  .pgpass: |
	//	    postgres:5432:registry:db_app:mypassword
	//	    postgres:5432:registry:db_migrator:otherpassword
	//
	// Then reference it:
	//
	//	pgpassSecretRef:
	//	  name: my-pgpass
	//	  key: .pgpass
	//
	// +optional
	PGPassSecretRef *corev1.SecretKeySelector `json:"pgpassSecretRef,omitempty"`

	// DisplayName is a human-readable name for the registry.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// PodTemplateSpec defines the pod template to use for the registry API server.
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the registry API server runs in, you must specify
	// the `registry-api` container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// ImagePullSecrets allows specifying image pull secrets for the registry API workload.
	// These are applied to both the registry-api Deployment's PodSpec.ImagePullSecrets
	// and to the operator-managed ServiceAccount the registry API runs as, so private
	// images are pullable through either path.
	//
	// Use this field for new manifests.
	//
	// Important: this is the ONLY way to attach image-pull credentials to the
	// operator-managed ServiceAccount. The legacy
	// spec.podTemplateSpec.spec.imagePullSecrets path populates the Deployment's pod
	// spec ONLY — it does NOT touch the ServiceAccount. On managed Kubernetes
	// platforms that rely on ServiceAccount-level credential injection (for example
	// GKE Workload Identity, OpenShift's per-SA dockercfg secrets, EKS IRSA), using
	// only the legacy PodTemplateSpec path can fail to pull private images even when
	// the secret exists in the namespace. Always set spec.imagePullSecrets when
	// SA-level credentials matter.
	//
	// Precedence with PodTemplateSpec:
	//   - This field is applied first as the controller-generated default.
	//   - Values set under spec.podTemplateSpec.spec.imagePullSecrets are user overrides
	//     and win on overlap. If the user supplies imagePullSecrets via PodTemplateSpec,
	//     those replace the default list on the Deployment (the list is treated atomically).
	//   - The ServiceAccount is always populated from this field — PodTemplateSpec does not
	//     affect the ServiceAccount.
	//
	// +listType=atomic
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// MCPRegistryStatus defines the observed state of MCPRegistry
type MCPRegistryStatus struct {
	// Conditions represent the latest available observations of the MCPRegistry's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current overall phase of the MCPRegistry
	// +optional
	Phase MCPRegistryPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// URL is the URL where the registry API can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// ReadyReplicas is the number of ready registry API replicas
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// MCPRegistryPhase represents the phase of the MCPRegistry
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating
type MCPRegistryPhase string

const (
	// MCPRegistryPhasePending means the MCPRegistry is being initialized
	MCPRegistryPhasePending MCPRegistryPhase = "Pending"

	// MCPRegistryPhaseReady means the MCPRegistry is ready and operational
	MCPRegistryPhaseReady MCPRegistryPhase = "Ready"

	// MCPRegistryPhaseFailed means the MCPRegistry has failed
	MCPRegistryPhaseFailed MCPRegistryPhase = "Failed"

	// MCPRegistryPhaseTerminating means the MCPRegistry is being deleted
	MCPRegistryPhaseTerminating MCPRegistryPhase = "Terminating"
)

// Condition reasons for MCPRegistry
const (
	// ConditionReasonRegistryReady indicates the MCPRegistry is ready
	ConditionReasonRegistryReady = "Ready"

	// ConditionReasonRegistryNotReady indicates the MCPRegistry is not ready
	ConditionReasonRegistryNotReady = "NotReady"
)

//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:shortName=mcpreg;registry,scope=Namespaced,categories=toolhive

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

// GetAPIResourceName returns the base name for registry API resources (deployment, service)
func (r *MCPRegistry) GetAPIResourceName() string {
	return fmt.Sprintf("%s-api", r.Name)
}

func init() {
	SchemeBuilder.Register(&MCPRegistry{}, &MCPRegistryList{})
}

// HasPodTemplateSpec returns true if the MCPRegistry has a PodTemplateSpec
func (r *MCPRegistry) HasPodTemplateSpec() bool {
	return r.Spec.PodTemplateSpec != nil
}

// GetPodTemplateSpecRaw returns the raw PodTemplateSpec
func (r *MCPRegistry) GetPodTemplateSpecRaw() *runtime.RawExtension {
	return r.Spec.PodTemplateSpec
}

// ParseVolumes deserializes the raw JSON Volumes into typed corev1.Volume objects.
// Returns an empty slice if Volumes is nil or empty.
func (s *MCPRegistrySpec) ParseVolumes() ([]corev1.Volume, error) {
	volumes := make([]corev1.Volume, 0, len(s.Volumes))
	for i, raw := range s.Volumes {
		var vol corev1.Volume
		if err := json.Unmarshal(raw.Raw, &vol); err != nil {
			return nil, fmt.Errorf("failed to unmarshal volumes[%d]: %w", i, err)
		}
		volumes = append(volumes, vol)
	}
	return volumes, nil
}

// ParseVolumeMounts deserializes the raw JSON VolumeMounts into typed corev1.VolumeMount objects.
// Returns an empty slice if VolumeMounts is nil or empty.
func (s *MCPRegistrySpec) ParseVolumeMounts() ([]corev1.VolumeMount, error) {
	mounts := make([]corev1.VolumeMount, 0, len(s.VolumeMounts))
	for i, raw := range s.VolumeMounts {
		var mount corev1.VolumeMount
		if err := json.Unmarshal(raw.Raw, &mount); err != nil {
			return nil, fmt.Errorf("failed to unmarshal volumeMounts[%d]: %w", i, err)
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}
