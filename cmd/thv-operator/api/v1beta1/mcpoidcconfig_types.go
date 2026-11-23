// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OIDC configuration source types for MCPOIDCConfig
const (
	// MCPOIDCConfigTypeKubernetesServiceAccount is the type for Kubernetes service account token validation
	MCPOIDCConfigTypeKubernetesServiceAccount MCPOIDCConfigSourceType = "kubernetesServiceAccount"

	// MCPOIDCConfigTypeInline is the type for inline OIDC configuration
	MCPOIDCConfigTypeInline MCPOIDCConfigSourceType = "inline"
)

// Condition type and reasons for MCPOIDCConfig status (RFC-0023)
const (
	// ConditionTypeOIDCConfigValid indicates whether the MCPOIDCConfig configuration is valid
	ConditionTypeOIDCConfigValid = ConditionTypeValid

	// ConditionReasonOIDCConfigValid indicates spec validation passed
	ConditionReasonOIDCConfigValid = "ConfigValid"

	// ConditionReasonOIDCConfigInvalid indicates spec validation failed
	ConditionReasonOIDCConfigInvalid = "ConfigInvalid"
)

// MCPOIDCConfigSourceType represents the type of OIDC configuration source for MCPOIDCConfig
type MCPOIDCConfigSourceType string

// MCPOIDCConfigSpec defines the desired state of MCPOIDCConfig.
// MCPOIDCConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources in the same namespace.
//
// +kubebuilder:validation:XValidation:rule="self.type == 'kubernetesServiceAccount' ? has(self.kubernetesServiceAccount) : !has(self.kubernetesServiceAccount)",message="kubernetesServiceAccount must be set when type is 'kubernetesServiceAccount', and must not be set otherwise"
// +kubebuilder:validation:XValidation:rule="self.type == 'inline' ? has(self.inline) : !has(self.inline)",message="inline must be set when type is 'inline', and must not be set otherwise"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPOIDCConfigSpec struct {
	// Type is the type of OIDC configuration source
	// +kubebuilder:validation:Enum=kubernetesServiceAccount;inline
	// +kubebuilder:validation:Required
	Type MCPOIDCConfigSourceType `json:"type"`

	// KubernetesServiceAccount configures OIDC for Kubernetes service account token validation.
	// Only used when Type is "kubernetesServiceAccount".
	// +optional
	KubernetesServiceAccount *KubernetesServiceAccountOIDCConfig `json:"kubernetesServiceAccount,omitempty"`

	// Inline contains direct OIDC configuration.
	// Only used when Type is "inline".
	// +optional
	Inline *InlineOIDCSharedConfig `json:"inline,omitempty"`
}

// KubernetesServiceAccountOIDCConfig configures OIDC for Kubernetes service account token validation.
// This contains shared fields without audience, which is specified per-server via MCPOIDCConfigReference.
type KubernetesServiceAccountOIDCConfig struct {
	// ServiceAccount restricts accepted tokens to a single service account.
	// When set, the token's "sub" claim must equal
	// system:serviceaccount:<namespace>:<serviceAccount>, where <namespace> is
	// Namespace if set, otherwise the namespace of the workload referencing this config.
	// When empty, tokens are not restricted to a specific service account.
	// Must be a valid service account name (DNS-1123 subdomain).
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// Namespace is the namespace of the service account(s) whose tokens are accepted.
	// When set without ServiceAccount, any service account in this namespace is accepted
	// (the "sub" claim must start with system:serviceaccount:<namespace>:).
	// When set with ServiceAccount, it selects that service account's namespace.
	// When both ServiceAccount and Namespace are empty, no subject restriction is applied
	// and any token from the issuer with a matching audience is accepted.
	// Must be a valid namespace name (DNS-1123 label).
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Issuer is the OIDC issuer URL.
	// +kubebuilder:default="https://kubernetes.default.svc"
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// JWKSURL is the URL to fetch the JWKS from.
	// If empty, OIDC discovery will be used to automatically determine the JWKS URL.
	// +optional
	JWKSURL string `json:"jwksUrl,omitempty"`

	// IntrospectionURL is the URL for token introspection endpoint.
	// If empty, OIDC discovery will be used to automatically determine the introspection URL.
	// +optional
	IntrospectionURL string `json:"introspectionUrl,omitempty"`

	// UseClusterAuth controls use of the Kubernetes cluster's CA bundle and the
	// workload's own service account token for OIDC discovery, JWKS and
	// introspection requests. The CA bundle is
	// /var/run/secrets/kubernetes.io/serviceaccount/ca.crt and the token is
	// /var/run/secrets/kubernetes.io/serviceaccount/token.
	//
	// When unset (the default), the CA bundle is used, and the token is sent only
	// if the issuer and any jwksUrl or introspectionUrl are https URLs on the
	// in-cluster API server (kubernetes.default.svc or
	// kubernetes.default.svc.cluster.local). It is not sent to any other host.
	// When true, the token is always sent; set this for an issuer that requires
	// it but is not reachable under those names, such as a custom cluster domain.
	// When false, neither the CA bundle nor the token is used.
	// +optional
	UseClusterAuth *bool `json:"useClusterAuth"`
}

// InlineOIDCSharedConfig contains direct OIDC configuration.
// This contains shared fields without audience and scopes, which are specified per-server
// via MCPOIDCConfigReference.
type InlineOIDCSharedConfig struct {
	// Issuer is the OIDC issuer URL
	// +kubebuilder:validation:Required
	Issuer string `json:"issuer"`

	// JWKSURL is the URL to fetch the JWKS from
	// +optional
	JWKSURL string `json:"jwksUrl,omitempty"`

	// IntrospectionURL is the URL for token introspection endpoint
	// +optional
	IntrospectionURL string `json:"introspectionUrl,omitempty"`

	// ClientID is the OIDC client ID
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef is a reference to a Kubernetes Secret containing the client secret
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// CABundleRef references a ConfigMap containing the CA certificate bundle.
	// When specified, ToolHive auto-mounts the ConfigMap and auto-computes ThvCABundlePath.
	// +optional
	CABundleRef *CABundleSource `json:"caBundleRef,omitempty"`

	// JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests
	// +optional
	JWKSAuthTokenPath string `json:"jwksAuthTokenPath,omitempty"`

	// JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses.
	// Note: at runtime, if either JWKSAllowPrivateIP or ProtectedResourceAllowPrivateIP
	// is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection).
	// +kubebuilder:default=false
	// +optional
	JWKSAllowPrivateIP bool `json:"jwksAllowPrivateIP"`

	// ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses.
	// Note: at runtime, if either ProtectedResourceAllowPrivateIP or JWKSAllowPrivateIP
	// is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection).
	// +kubebuilder:default=false
	// +optional
	ProtectedResourceAllowPrivateIP bool `json:"protectedResourceAllowPrivateIP"`

	// InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuer and JWKS URLs for development/testing.
	// WARNING: This is insecure and should NEVER be used in production.
	// +kubebuilder:default=false
	// +optional
	InsecureAllowHTTP bool `json:"insecureAllowHTTP"`
}

// Well-known WorkloadReference Kind values.
const (
	WorkloadKindMCPServer        = "MCPServer"
	WorkloadKindVirtualMCPServer = "VirtualMCPServer"
	WorkloadKindMCPRemoteProxy   = "MCPRemoteProxy"
)

// WorkloadReference identifies a workload that references a shared configuration resource.
// Namespace is implicit — cross-namespace references are not supported.
type WorkloadReference struct {
	// Kind is the type of workload resource
	// +kubebuilder:validation:Enum=MCPServer;VirtualMCPServer;MCPRemoteProxy
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name is the name of the workload resource
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// MCPOIDCConfigStatus defines the observed state of MCPOIDCConfig
type MCPOIDCConfigStatus struct {
	// Conditions represent the latest available observations of the MCPOIDCConfig's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this MCPOIDCConfig.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
// +kubebuilder:resource:shortName=mcpoidc,categories=toolhive
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPOIDCConfig is the Schema for the mcpoidcconfigs API.
// MCPOIDCConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources within the same namespace. Cross-namespace references
// are not supported for security and isolation reasons.
type MCPOIDCConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPOIDCConfigSpec   `json:"spec,omitempty"`
	Status MCPOIDCConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPOIDCConfigList contains a list of MCPOIDCConfig
type MCPOIDCConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPOIDCConfig `json:"items"`
}

// MCPOIDCConfigReference is a reference to an MCPOIDCConfig resource with per-server overrides.
// The referenced MCPOIDCConfig must be in the same namespace as the MCPServer.
type MCPOIDCConfigReference struct {
	// Name is the name of the MCPOIDCConfig resource
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Audience is the expected audience for token validation.
	// This MUST be unique per server to prevent token replay attacks.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Audience string `json:"audience"`

	// Scopes is the list of OAuth scopes to advertise in the well-known endpoint (RFC 9728).
	// If empty, defaults to ["openid"].
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// ResourceURL is the public URL for OAuth protected resource metadata (RFC 9728).
	// When the server is exposed via Ingress or gateway, set this to the external
	// URL that MCP clients connect to. If not specified, defaults to the internal
	// Kubernetes service URL.
	// +optional
	ResourceURL string `json:"resourceUrl,omitempty"`
}

// Validate performs validation on the MCPOIDCConfig spec.
// This method is called by the controller during reconciliation.
//
// Note: These validations provide defense-in-depth alongside CEL validation rules.
// CEL catches issues at API admission time, but this method also validates stored objects
// to catch any that bypassed CEL or were stored before CEL rules were added.
func (r *MCPOIDCConfig) Validate() error {
	return r.validateTypeConfigConsistency()
}

// validateTypeConfigConsistency validates that the correct config is set for the selected type.
// This mirrors the CEL validation rules but provides defense-in-depth for stored objects.
func (r *MCPOIDCConfig) validateTypeConfigConsistency() error {
	if (r.Spec.KubernetesServiceAccount == nil) == (r.Spec.Type == MCPOIDCConfigTypeKubernetesServiceAccount) {
		return fmt.Errorf("kubernetesServiceAccount configuration must be set if and only if type is 'kubernetesServiceAccount'")
	}
	if (r.Spec.Inline == nil) == (r.Spec.Type == MCPOIDCConfigTypeInline) {
		return fmt.Errorf("inline configuration must be set if and only if type is 'inline'")
	}
	return nil
}

func init() {
	SchemeBuilder.Register(&MCPOIDCConfig{}, &MCPOIDCConfigList{})
}
