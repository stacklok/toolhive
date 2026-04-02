// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/telemetry"
)

// SensitiveHeader represents a header whose value is stored in a Kubernetes Secret.
// This allows credential headers (e.g., API keys, bearer tokens) to be securely
// referenced without embedding secrets inline in the MCPTelemetryConfig resource.
type SensitiveHeader struct {
	// Name is the header name (e.g., "Authorization", "X-API-Key")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// SecretKeyRef is a reference to a Kubernetes Secret key containing the header value
	// +kubebuilder:validation:Required
	SecretKeyRef SecretKeyRef `json:"secretKeyRef"`
}

// MCPTelemetryConfigSpec defines the desired state of MCPTelemetryConfig.
// It embeds telemetry.Config from pkg/telemetry to eliminate the conversion
// layer between CRD and application types. The environmentVariables field is
// CLI-only and rejected by CEL validation; customAttributes is allowed for
// setting shared OTel resource attributes (e.g., deployment.environment).
//
// +kubebuilder:validation:XValidation:rule="!has(self.environmentVariables)",message="environmentVariables is a CLI-only field and cannot be set in MCPTelemetryConfig; use customAttributes for resource attributes"
// +kubebuilder:validation:XValidation:rule="!has(self.headers) || !has(self.sensitiveHeaders) || self.sensitiveHeaders.all(sh, !(sh.name in self.headers))",message="a header name cannot appear in both headers and sensitiveHeaders"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPTelemetryConfigSpec struct {
	telemetry.Config `json:",inline"` // nolint:revive

	// SensitiveHeaders contains headers whose values are stored in Kubernetes Secrets.
	// Use this for credential headers (e.g., API keys, bearer tokens) instead of
	// embedding secrets in the headers field.
	// +optional
	SensitiveHeaders []SensitiveHeader `json:"sensitiveHeaders,omitempty"`
}

// MCPTelemetryConfigStatus defines the observed state of MCPTelemetryConfig
type MCPTelemetryConfigStatus struct {
	// Conditions represent the latest available observations of the MCPTelemetryConfig's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPTelemetryConfig.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingServers is a list of MCPServer resources that reference this MCPTelemetryConfig
	// +optional
	ReferencingServers []string `json:"referencingServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcpotel,categories=toolhive
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoint`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
// +kubebuilder:printcolumn:name="References",type=string,JSONPath=`.status.referencingServers`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPTelemetryConfig is the Schema for the mcptelemetryconfigs API.
// MCPTelemetryConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources within the same namespace. Cross-namespace references
// are not supported for security and isolation reasons.
type MCPTelemetryConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPTelemetryConfigSpec   `json:"spec,omitempty"`
	Status MCPTelemetryConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPTelemetryConfigList contains a list of MCPTelemetryConfig
type MCPTelemetryConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPTelemetryConfig `json:"items"`
}

// MCPTelemetryConfigReference is a reference to an MCPTelemetryConfig resource
// with per-server overrides. The referenced MCPTelemetryConfig must be in the
// same namespace as the MCPServer.
type MCPTelemetryConfigReference struct {
	// Name is the name of the MCPTelemetryConfig resource
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ServiceName overrides the telemetry service name for this specific server.
	// This MUST be unique per server for proper observability (e.g., distinguishing
	// traces and metrics from different servers sharing the same collector).
	// If empty, defaults to the server name with "thv-" prefix at runtime.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`
}

// Validate performs validation on the MCPTelemetryConfig spec.
// This provides defense-in-depth alongside CEL validation rules.
// CEL catches issues at API admission time, but this method also validates
// stored objects to catch any that bypassed CEL or were stored before CEL rules were added.
func (r *MCPTelemetryConfig) Validate() error {
	if err := r.validateCLIOnlyFields(); err != nil {
		return err
	}
	return r.validateSensitiveHeaders()
}

// validateCLIOnlyFields rejects CLI-only fields that are not applicable to CRD-managed telemetry.
func (r *MCPTelemetryConfig) validateCLIOnlyFields() error {
	if len(r.Spec.EnvironmentVariables) > 0 {
		return fmt.Errorf("environmentVariables is a CLI-only field and cannot be set in MCPTelemetryConfig")
	}
	return nil
}

// validateSensitiveHeaders validates sensitive header entries and checks for overlap with plaintext headers.
func (r *MCPTelemetryConfig) validateSensitiveHeaders() error {
	for i, sh := range r.Spec.SensitiveHeaders {
		if sh.Name == "" {
			return fmt.Errorf("sensitiveHeaders[%d].name must not be empty", i)
		}
		if sh.SecretKeyRef.Name == "" {
			return fmt.Errorf("sensitiveHeaders[%d].secretKeyRef.name must not be empty", i)
		}
		if sh.SecretKeyRef.Key == "" {
			return fmt.Errorf("sensitiveHeaders[%d].secretKeyRef.key must not be empty", i)
		}
		if _, exists := r.Spec.Headers[sh.Name]; exists {
			return fmt.Errorf("header %q appears in both headers and sensitiveHeaders", sh.Name)
		}
	}
	return nil
}

func init() {
	SchemeBuilder.Register(&MCPTelemetryConfig{}, &MCPTelemetryConfigList{})
}
