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

	// ExternalAuthTypeUnauthenticated is the type for no authentication
	// This should only be used for backends on trusted networks (e.g., localhost, VPC)
	// or when authentication is handled by network-level security
	ExternalAuthTypeUnauthenticated ExternalAuthType = "unauthenticated"

	// ExternalAuthTypeAWSSts is the type for AWS STS authentication
	ExternalAuthTypeAWSSts ExternalAuthType = "awsSts"
)

// ExternalAuthType represents the type of external authentication
type ExternalAuthType string

// MCPExternalAuthConfigSpec defines the desired state of MCPExternalAuthConfig.
// MCPExternalAuthConfig resources are namespace-scoped and can only be referenced by
// MCPServer resources in the same namespace.
type MCPExternalAuthConfigSpec struct {
	// Type is the type of external authentication to configure
	// +kubebuilder:validation:Enum=tokenExchange;headerInjection;unauthenticated;awsSts
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

	// AWSSts configures AWS STS authentication with SigV4 request signing
	// Only used when Type is "awsSts"
	// +optional
	AWSSts *AWSStsConfig `json:"awsSts,omitempty"`
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
// This allows injecting a secret-based header value into requests to backend MCP servers.
// For security reasons, only secret references are supported (no plaintext values).
type HeaderInjectionConfig struct {
	// HeaderName is the name of the HTTP header to inject
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	HeaderName string `json:"headerName"`

	// ValueSecretRef references a Kubernetes Secret containing the header value
	// +kubebuilder:validation:Required
	ValueSecretRef *SecretKeyRef `json:"valueSecretRef"`
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

// AWSStsConfig holds configuration for AWS STS authentication with SigV4 request signing.
// This configuration exchanges incoming authentication tokens (typically OIDC JWT) for AWS STS
// temporary credentials, then signs requests to AWS services using SigV4.
type AWSStsConfig struct {
	// Region is the AWS region for the STS endpoint and service
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// Service is the AWS service name for SigV4 signing
	// Defaults to "execute-api" for API Gateway endpoints
	// +kubebuilder:default="execute-api"
	// +optional
	Service string `json:"service,omitempty"`

	// RoleArn is the default IAM role ARN to assume
	// This role is used when no role mappings match or RoleMappings is empty
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^arn:aws:iam::\d{12}:role/[a-zA-Z0-9+=,.@\-_/]+$`
	RoleArn string `json:"roleArn"`

	// RoleMappings defines claim-based role selection rules
	// Allows mapping JWT claims (e.g., groups, roles) to specific IAM roles
	// Higher priority mappings are evaluated first
	// +optional
	RoleMappings []RoleMapping `json:"roleMappings,omitempty"`

	// RoleClaim is the JWT claim to use for role mapping evaluation
	// Defaults to "groups" to match common OIDC group claims
	// +kubebuilder:default="groups"
	// +optional
	RoleClaim string `json:"roleClaim,omitempty"`

	// SessionDuration is the duration in seconds for the STS session
	// Must be between 900 (15 minutes) and 43200 (12 hours)
	// Defaults to 3600 (1 hour) if not specified
	// +kubebuilder:validation:Minimum=900
	// +kubebuilder:validation:Maximum=43200
	// +kubebuilder:default=3600
	// +optional
	SessionDuration *int32 `json:"sessionDuration,omitempty"`

	// SessionTags are AWS session tags to pass to AssumeRole
	// Tags can use static values or be sourced from JWT claims
	// +optional
	SessionTags []SessionTag `json:"sessionTags,omitempty"`
}

// RoleMapping defines a rule for mapping JWT claims to IAM roles.
// Mappings are evaluated in priority order (highest first), and the first
// matching rule determines which IAM role to assume.
type RoleMapping struct {
	// Claim is the claim value to match against
	// The claim type is specified by AWSStsConfig.RoleClaim
	// For example, if RoleClaim is "groups", this would be a group name
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Claim string `json:"claim"`

	// RoleArn is the IAM role ARN to assume when this mapping matches
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^arn:aws:iam::\d{12}:role/[a-zA-Z0-9+=,.@\-_/]+$`
	RoleArn string `json:"roleArn"`

	// Priority determines evaluation order (higher values evaluated first)
	// Allows fine-grained control over role selection precedence
	// Defaults to 0 if not specified
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	Priority *int32 `json:"priority,omitempty"`
}

// SessionTag represents an AWS session tag that can be passed to AssumeRole.
// Tags can have static values or be dynamically sourced from JWT claims.
type SessionTag struct {
	// Key is the session tag key
	// Must comply with AWS session tag key requirements
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[\w+=,.@-]+$`
	Key string `json:"key"`

	// Value is the static value for the session tag
	// Used when ClaimSource is not specified
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Value string `json:"value,omitempty"`

	// ClaimSource is the JWT claim to use as the tag value
	// When specified, the tag value is sourced from this claim in the incoming JWT
	// Takes precedence over the static Value field
	// +optional
	ClaimSource string `json:"claimSource,omitempty"`
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
