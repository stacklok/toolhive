package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// External auth configuration types
const (
	// ExternalAuthTypeTokenExchange is the type for RFC-8693 token exchange
	ExternalAuthTypeTokenExchange ExternalAuthType = "tokenExchange"

	// ExternalAuthTypeHeaderInjection is the type for custom header injection
	ExternalAuthTypeHeaderInjection ExternalAuthType = "headerInjection"
)

// ExternalAuthType represents the type of external authentication
type ExternalAuthType string

// MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
// MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources in the same namespace.
type MCPExternalAuthConfigSpec struct {
	// Type is the type of external authentication to configure
	// +kubebuilder:validation:Enum=tokenExchange;headerInjection
	// +kubebuilder:validation:Required
	Type ExternalAuthType `json:"type"`

	// TokenExchange configures RFC-8693 OAuth 2.0 Token Exchange
	// Only used when Type is "tokenExchange"
	// +optional
	TokenExchange *TokenExchangeConfig `json:"tokenExchange,omitempty"`

	// HeaderInjection configures custom HTTP header injection
	// Only used when Type is "headerInjection"
	// +optional
	HeaderInjection *HeaderInjectionConfig `json:"headerInjection,omitempty"`
}

// TokenExchangeConfig holds configuration for RFC-8693 OAuth 2.0 Token Exchange.
// This configuration is used to exchange incoming authentication tokens for tokens
// that can be used with external services.
// The structure matches the tokenexchange.Config from pkg/auth/tokenexchange/middleware.go
type TokenExchangeConfig struct {
	// TokenURL is the OAuth 2.0 token endpoint URL for token exchange
	// +kubebuilder:validation:Required
	TokenURL string `json:"tokenUrl"`

	// ClientID is the OAuth 2.0 client identifier
	// Optional for some token exchange flows (e.g., Google Cloud Workforce Identity)
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef is a reference to a secret containing the OAuth 2.0 client secret
	// Optional for some token exchange flows (e.g., Google Cloud Workforce Identity)
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// Audience is the target audience for the exchanged token
	// +kubebuilder:validation:Required
	Audience string `json:"audience"`

	// Scopes is a list of OAuth 2.0 scopes to request for the exchanged token
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// SubjectTokenType is the type of the incoming subject token.
	// Accepts short forms: "access_token" (default), "id_token", "jwt"
	// Or full URNs: "urn:ietf:params:oauth:token-type:access_token",
	//               "urn:ietf:params:oauth:token-type:id_token",
	//               "urn:ietf:params:oauth:token-type:jwt"
	// For Google Workload Identity Federation with OIDC providers (like Okta), use "id_token"
	// +kubebuilder:validation:Pattern=`^(access_token|id_token|jwt|urn:ietf:params:oauth:token-type:(access_token|id_token|jwt))?$`
	// +optional
	SubjectTokenType string `json:"subjectTokenType,omitempty"`

	// ExternalTokenHeaderName is the name of the custom header to use for the exchanged token.
	// If set, the exchanged token will be added to this custom header (e.g., "X-Upstream-Token").
	// If empty or not set, the exchanged token will replace the Authorization header (default behavior).
	// +optional
	ExternalTokenHeaderName string `json:"externalTokenHeaderName,omitempty"`
}

// HeaderInjectionConfig holds configuration for custom HTTP header injection authentication.
// This allows injecting a static or secret-based header value into requests to backend MCP servers.
// +kubebuilder:validation:XValidation:rule="(has(self.value) && self.value != '') != has(self.valueSecretRef)",message="Either value or valueSecretRef must be specified, but not both"
type HeaderInjectionConfig struct {
	// HeaderName is the name of the HTTP header to inject
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	HeaderName string `json:"headerName"`

	// Value is the header value (for non-sensitive data)
	// Either Value or ValueSecretRef must be specified, but not both
	// +optional
	Value string `json:"value,omitempty"`

	// ValueSecretRef references a Kubernetes Secret containing the header value (recommended for sensitive data)
	// Either Value or ValueSecretRef must be specified, but not both
	// +optional
	ValueSecretRef *SecretKeyRef `json:"valueSecretRef,omitempty"`
}

// SecretKeyRef is a reference to a key within a Secret
type SecretKeyRef struct {
	// Name is the name of the secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key within the secret
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// MCPExternalAuthConfigStatus defines the observed state of MCPExternalAuthConfig
type MCPExternalAuthConfigStatus struct {
	// ObservedGeneration is the most recent generation observed for this MCPExternalAuthConfig.
	// It corresponds to the MCPExternalAuthConfig's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ConfigHash is a hash of the current configuration for change detection
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// ReferencingServers is a list of MCPServer resources that reference this MCPExternalAuthConfig
	// This helps track which servers need to be reconciled when this config changes
	// +optional
	ReferencingServers []string `json:"referencingServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=extauth;mcpextauth
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPExternalAuthConfig is the Schema for the mcpexternalauthconfigs API.
// MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources within the same namespace. Cross-namespace references
// are not supported for security and isolation reasons.
type MCPExternalAuthConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPExternalAuthConfigSpec   `json:"spec,omitempty"`
	Status MCPExternalAuthConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig
type MCPExternalAuthConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPExternalAuthConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPExternalAuthConfig{}, &MCPExternalAuthConfigList{})
}
