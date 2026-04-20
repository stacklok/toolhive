// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// maxK8sVolumeName is the maximum length for a Kubernetes volume name (RFC 1123 label).
	maxK8sVolumeName = 63
	// telemetryCABundleVolumePrefix must match validation.TelemetryCABundleVolumePrefix.
	telemetryCABundleVolumePrefix = "otel-ca-bundle-"
	// maxTelemetryCABundleConfigMapName is the maximum ConfigMap name length that fits in a volume name.
	maxTelemetryCABundleConfigMapName = maxK8sVolumeName - len(telemetryCABundleVolumePrefix)
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

// MCPTelemetryOTelConfig defines OpenTelemetry configuration for shared MCPTelemetryConfig resources.
// Unlike OpenTelemetryConfig (used by inline MCPServer telemetry), this type:
//   - Omits ServiceName (per-server field set via MCPTelemetryConfigReference)
//   - Uses map[string]string for Headers (not []string)
//   - Adds SensitiveHeaders for Kubernetes Secret-backed credentials
//   - Adds ResourceAttributes for shared OTel resource attributes
//
// +kubebuilder:validation:XValidation:rule="!has(self.headers) || !has(self.sensitiveHeaders) || self.sensitiveHeaders.all(sh, !(sh.name in self.headers))",message="a header name cannot appear in both headers and sensitiveHeaders"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPTelemetryOTelConfig struct {
	// Enabled controls whether OpenTelemetry is enabled
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Endpoint is the OTLP endpoint URL for tracing and metrics
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint
	// +kubebuilder:default=false
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// Headers contains authentication headers for the OTLP endpoint.
	// For secret-backed credentials, use sensitiveHeaders instead.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// SensitiveHeaders contains headers whose values are stored in Kubernetes Secrets.
	// Use this for credential headers (e.g., API keys, bearer tokens) instead of
	// embedding secrets in the headers field.
	// +listType=map
	// +listMapKey=name
	// +optional
	SensitiveHeaders []SensitiveHeader `json:"sensitiveHeaders,omitempty"`

	// ResourceAttributes contains custom resource attributes to be added to all telemetry signals.
	// These become OTel resource attributes (e.g., deployment.environment, service.namespace).
	// Note: service.name is intentionally excluded — it is set per-server via
	// MCPTelemetryConfigReference.ServiceName.
	// +optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`

	// Metrics defines OpenTelemetry metrics-specific configuration
	// +optional
	Metrics *OpenTelemetryMetricsConfig `json:"metrics,omitempty"`

	// Tracing defines OpenTelemetry tracing configuration
	// +optional
	Tracing *OpenTelemetryTracingConfig `json:"tracing,omitempty"`

	// UseLegacyAttributes controls whether legacy attribute names are emitted alongside
	// the new MCP OTEL semantic convention names. Defaults to true for backward compatibility.
	// This will change to false in a future release and eventually be removed.
	// +kubebuilder:default=true
	// +optional
	UseLegacyAttributes bool `json:"useLegacyAttributes"`

	// CABundleRef references a ConfigMap containing a CA certificate bundle for the OTLP endpoint.
	// When specified, the operator mounts the ConfigMap into the proxyrunner pod and configures
	// the OTLP exporters to trust the custom CA. This is useful when the OTLP collector uses
	// TLS with certificates signed by an internal or private CA.
	// +optional
	CABundleRef *CABundleSource `json:"caBundleRef,omitempty"`
}

// MCPTelemetryConfigSpec defines the desired state of MCPTelemetryConfig.
// The spec uses a nested structure with openTelemetry and prometheus sub-objects
// for clear separation of concerns.
type MCPTelemetryConfigSpec struct {
	// OpenTelemetry defines OpenTelemetry configuration (OTLP endpoint, tracing, metrics)
	// +optional
	OpenTelemetry *MCPTelemetryOTelConfig `json:"openTelemetry,omitempty"`

	// Prometheus defines Prometheus-specific configuration
	// +optional
	Prometheus *PrometheusConfig `json:"prometheus,omitempty"`
}

// MCPTelemetryConfigStatus defines the observed state of MCPTelemetryConfig
type MCPTelemetryConfigStatus struct {
	// Conditions represent the latest available observations of the MCPTelemetryConfig's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPTelemetryConfig.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingWorkloads lists workloads that reference this MCPTelemetryConfig
	// +listType=map
	// +listMapKey=name
	// +optional
	ReferencingWorkloads []WorkloadReference `json:"referencingWorkloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mcpotel,categories=toolhive
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.openTelemetry.endpoint`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
// +kubebuilder:printcolumn:name="Tracing",type=boolean,JSONPath=`.spec.openTelemetry.tracing.enabled`
// +kubebuilder:printcolumn:name="Metrics",type=boolean,JSONPath=`.spec.openTelemetry.metrics.enabled`
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
	if err := r.validateEndpointRequiresSignals(); err != nil {
		return err
	}
	if err := r.validateSensitiveHeaders(); err != nil {
		return err
	}
	return r.validateCABundle()
}

// validateEndpointRequiresSignals rejects an endpoint when neither tracing nor metrics is enabled.
// Without this check the config would pass CRD validation but fail at runtime in telemetry.NewProvider.
func (r *MCPTelemetryConfig) validateEndpointRequiresSignals() error {
	if r.Spec.OpenTelemetry == nil {
		return nil
	}
	otel := r.Spec.OpenTelemetry
	if otel.Endpoint == "" {
		return nil
	}
	tracingEnabled := otel.Tracing != nil && otel.Tracing.Enabled
	metricsEnabled := otel.Metrics != nil && otel.Metrics.Enabled
	if !tracingEnabled && !metricsEnabled {
		return fmt.Errorf("endpoint requires at least one of tracing or metrics to be enabled")
	}
	return nil
}

// validateSensitiveHeaders validates sensitive header entries and checks for overlap with plaintext headers.
func (r *MCPTelemetryConfig) validateSensitiveHeaders() error {
	if r.Spec.OpenTelemetry == nil {
		return nil
	}
	otel := r.Spec.OpenTelemetry
	for i, sh := range otel.SensitiveHeaders {
		if sh.Name == "" {
			return fmt.Errorf("openTelemetry.sensitiveHeaders[%d].name must not be empty", i)
		}
		if sh.SecretKeyRef.Name == "" {
			return fmt.Errorf("openTelemetry.sensitiveHeaders[%d].secretKeyRef.name must not be empty", i)
		}
		if sh.SecretKeyRef.Key == "" {
			return fmt.Errorf("openTelemetry.sensitiveHeaders[%d].secretKeyRef.key must not be empty", i)
		}
		if _, exists := otel.Headers[sh.Name]; exists {
			return fmt.Errorf("header %q appears in both headers and sensitiveHeaders", sh.Name)
		}
	}
	return nil
}

// validateCABundle validates the CA bundle configuration if present.
func (r *MCPTelemetryConfig) validateCABundle() error {
	if r.Spec.OpenTelemetry == nil || r.Spec.OpenTelemetry.CABundleRef == nil {
		return nil
	}
	otel := r.Spec.OpenTelemetry
	if otel.Insecure {
		return fmt.Errorf("openTelemetry.caBundleRef cannot be specified when insecure is true; they are mutually exclusive")
	}
	ref := otel.CABundleRef
	if ref.ConfigMapRef == nil {
		return fmt.Errorf("openTelemetry.caBundleRef.configMapRef must be specified")
	}
	if ref.ConfigMapRef.Name == "" {
		return fmt.Errorf("openTelemetry.caBundleRef.configMapRef.name must not be empty")
	}
	if len(ref.ConfigMapRef.Name) > maxTelemetryCABundleConfigMapName {
		//nolint:lll // error message clarity requires full context
		return fmt.Errorf(
			"openTelemetry.caBundleRef.configMapRef.name %q is too long (%d chars); maximum is %d",
			ref.ConfigMapRef.Name, len(ref.ConfigMapRef.Name), maxTelemetryCABundleConfigMapName,
		)
	}
	return nil
}

func init() {
	SchemeBuilder.Register(&MCPTelemetryConfig{}, &MCPTelemetryConfigList{})
}
