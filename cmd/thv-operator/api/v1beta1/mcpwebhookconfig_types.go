// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// WebhookFailurePolicy defines how webhook errors are handled.
type WebhookFailurePolicy string

const (
	// WebhookFailurePolicyFail denies the request on webhook error.
	WebhookFailurePolicyFail WebhookFailurePolicy = "fail"
	// WebhookFailurePolicyIgnore allows the request on webhook error.
	WebhookFailurePolicyIgnore WebhookFailurePolicy = "ignore"
)

// WebhookTLSConfig contains TLS configuration for secure webhook connections
type WebhookTLSConfig struct {
	// CASecretRef references a Secret containing the CA certificate bundle used to verify the webhook server's certificate.
	// Contains a bundle of PEM-encoded X.509 certificates.
	// +optional
	CASecretRef *SecretKeyRef `json:"caSecretRef,omitempty"`

	// ClientCertSecretRef references a Secret containing the client certificate for mTLS authentication.
	// The referenced key must contain a PEM-encoded client certificate.
	// Use ClientKeySecretRef to provide the corresponding private key.
	// +optional
	ClientCertSecretRef *SecretKeyRef `json:"clientCertSecretRef,omitempty"`

	// ClientKeySecretRef references a Secret containing the private key for the client certificate.
	// Required when ClientCertSecretRef is set to enable mTLS.
	// +optional
	ClientKeySecretRef *SecretKeyRef `json:"clientKeySecretRef,omitempty"`

	// InsecureSkipVerify disables server certificate verification.
	// WARNING: This should only be used for development/testing and not in production environments.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// WebhookSpec defines the configuration for a single webhook middleware
type WebhookSpec struct {
	// Name is a unique identifier for this webhook
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// URL is the endpoint to call for this webhook. Must be an HTTP/HTTPS URL.
	// +kubebuilder:validation:Format=uri
	URL string `json:"url"`

	// Timeout configures the maximum time to wait for the webhook to respond.
	// Defaults to 10s if not specified. Maximum is 30s.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=duration
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// FailurePolicy defines how to handle errors when communicating with the webhook.
	// Supported values: "fail", "ignore". Defaults to "fail".
	// +kubebuilder:validation:Enum=fail;ignore
	// +kubebuilder:default=fail
	// +optional
	FailurePolicy WebhookFailurePolicy `json:"failurePolicy,omitempty"`

	// TLSConfig contains optional TLS configuration for the webhook connection.
	// +optional
	TLSConfig *WebhookTLSConfig `json:"tlsConfig,omitempty"`

	// HMACSecretRef references a Kubernetes Secret containing the HMAC signing key
	// used to sign the webhook payload. If set, the X-Toolhive-Signature header will be injected.
	// +optional
	HMACSecretRef *SecretKeyRef `json:"hmacSecretRef,omitempty"`
}

// MCPWebhookConfigSpec defines the desired state of MCPWebhookConfig
// +kubebuilder:validation:XValidation:rule="(has(self.validating) ? size(self.validating) : 0) + (has(self.mutating) ? size(self.mutating) : 0) > 0",message="at least one validating or mutating webhook must be defined"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPWebhookConfigSpec struct {
	// Validating webhooks are called to approve or deny MCP requests.
	// +optional
	Validating []WebhookSpec `json:"validating,omitempty"`

	// Mutating webhooks are called to transform MCP requests before processing.
	// +optional
	Mutating []WebhookSpec `json:"mutating,omitempty"`
}

// MCPWebhookConfigStatus defines the observed state of MCPWebhookConfig
type MCPWebhookConfigStatus struct {
	// Conditions represent the latest available observations
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last observed generation corresponding to the current status
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the spec, used for detecting changes
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingWorkloads is a list of workload resources that reference this MCPWebhookConfig.
	// Each entry identifies the workload by kind and name.
	// +listType=map
	// +listMapKey=name
	// +optional
	ReferencingWorkloads []WorkloadReference `json:"referencingWorkloads,omitempty"`
}
