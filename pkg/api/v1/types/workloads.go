// Package types contains types for the API v1.
package types

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// WorkloadListResponse represents the response for listing workloads
//
//	@Description	Response containing a list of workloads
type WorkloadListResponse struct {
	// List of container information for each workload
	Workloads []core.Workload `json:"workloads"`
}

// WorkloadStatusResponse represents the response for getting workload status
//
//	@Description	Response containing workload status information
type WorkloadStatusResponse struct {
	// Current status of the workload
	Status runtime.WorkloadStatus `json:"status"`
}

// UpdateRequest represents the request to update an existing workload
//
//	@Description	Request to update an existing workload (name cannot be changed)
type UpdateRequest struct {
	// Docker image to use
	Image string `json:"image"`
	// Host to bind to
	Host string `json:"host"`
	// Command arguments to pass to the container
	CmdArguments []string `json:"cmd_arguments"`
	// Port to expose from the container
	TargetPort int `json:"target_port"`
	// Environment variables to set in the container
	EnvVars map[string]string `json:"env_vars"`
	// Secret parameters to inject
	Secrets []secrets.SecretParameter `json:"secrets"`
	// Volume mounts
	Volumes []string `json:"volumes"`
	// Transport configuration
	Transport string `json:"transport"`
	// Authorization configuration
	AuthzConfig string `json:"authz_config"`
	// OIDC configuration options
	OIDC oidcOptions `json:"oidc"`
	// Permission profile to apply
	PermissionProfile *permissions.Profile `json:"permission_profile"`
	// Proxy mode to use
	ProxyMode string `json:"proxy_mode"`
	// Whether network isolation is turned on. This applies the rules in the permission profile.
	NetworkIsolation bool `json:"network_isolation"`
	// Tools filter
	ToolsFilter []string `json:"tools"`
	// Group name this workload belongs to
	Group string `json:"group,omitempty"`

	// Remote server specific fields
	URL         string             `json:"url,omitempty"`
	OAuthConfig *RemoteOAuthConfig `json:"oauth_config,omitempty"`
	Headers     []*registry.Header `json:"headers,omitempty"`
}

// RemoteOAuthConfig represents OAuth configuration for remote servers
//
//	@Description	OAuth configuration for remote server authentication
type RemoteOAuthConfig struct {
	// OAuth/OIDC issuer URL (e.g., https://accounts.google.com)
	Issuer string `json:"issuer,omitempty"`
	// OAuth authorization endpoint URL (alternative to issuer for non-OIDC OAuth)
	AuthorizeURL string `json:"authorize_url,omitempty"`
	// OAuth token endpoint URL (alternative to issuer for non-OIDC OAuth)
	TokenURL string `json:"token_url,omitempty"`
	// OAuth client ID for authentication
	ClientID     string                   `json:"client_id,omitempty"`
	ClientSecret *secrets.SecretParameter `json:"client_secret,omitempty"`

	// OAuth scopes to request
	Scopes []string `json:"scopes,omitempty"`
	// Whether to use PKCE for the OAuth flow
	UsePKCE bool `json:"use_pkce,omitempty"`
	// Additional OAuth parameters for server-specific customization
	OAuthParams map[string]string `json:"oauth_params,omitempty"`
	// Specific port for OAuth callback server
	CallbackPort int `json:"callback_port,omitempty"`
}

// CreateRequest represents the request to create a new workload
//
//	@Description	Request to create a new workload
type CreateRequest struct {
	UpdateRequest
	// Name of the workload
	Name string `json:"name"`
}

// oidcOptions represents OIDC configuration options
//
//	@Description	OIDC configuration for workload authentication
type oidcOptions struct {
	// OIDC issuer URL
	Issuer string `json:"issuer"`
	// Expected audience
	Audience string `json:"audience"`
	// JWKS URL for key verification
	JwksURL string `json:"jwks_url"`
	// Token introspection URL for OIDC
	IntrospectionURL string `json:"introspection_url"`
	// OAuth2 client ID
	ClientID string `json:"client_id"`
	// OAuth2 client secret
	ClientSecret string `json:"client_secret"`
}

// CreateWorkloadResponse represents the response for workload creation
//
//	@Description	Response after successfully creating a workload
type CreateWorkloadResponse struct {
	// Name of the created workload
	Name string `json:"name"`
	// Port the workload is listening on
	Port int `json:"port"`
}

// BulkOperationRequest represents a request for bulk operations on workloads
type BulkOperationRequest struct {
	// Names of the workloads to operate on
	Names []string `json:"names"`
	// Group name to operate on (mutually exclusive with names)
	Group string `json:"group,omitempty"`
}

// ValidateBulkOperationRequest validates the bulk operation request
func ValidateBulkOperationRequest(req BulkOperationRequest) error {
	if len(req.Names) > 0 && req.Group != "" {
		return fmt.Errorf("cannot specify both names and group")
	}
	if len(req.Names) == 0 && req.Group == "" {
		return fmt.Errorf("must specify either names or group")
	}
	return nil
}

// RunConfigToCreateRequest converts a RunConfig to createRequest for API responses
func RunConfigToCreateRequest(runConfig *runner.RunConfig) *CreateRequest {
	// Convert CLI secrets ([]string) back to SecretParameters
	secretParams := make([]secrets.SecretParameter, 0, len(runConfig.Secrets))
	for _, secretStr := range runConfig.Secrets {
		// Parse the CLI format: "<name>,target=<target>"
		if secretParam, err := secrets.ParseSecretParameter(secretStr); err == nil {
			secretParams = append(secretParams, secretParam)
		}
		// Ignore invalid secrets rather than failing the entire conversion
	}

	// Get OIDC fields from RunConfig
	var oidcConfig oidcOptions
	if runConfig.OIDCConfig != nil {
		oidcConfig = oidcOptions{
			Issuer:           runConfig.OIDCConfig.Issuer,
			Audience:         runConfig.OIDCConfig.Audience,
			JwksURL:          runConfig.OIDCConfig.JWKSURL,
			IntrospectionURL: runConfig.OIDCConfig.IntrospectionURL,
			ClientID:         runConfig.OIDCConfig.ClientID,
			ClientSecret:     runConfig.OIDCConfig.ClientSecret,
		}
	}

	// Get remote OAuth config from RunConfig
	var remoteOAuthConfig *RemoteOAuthConfig
	var headers []*registry.Header
	if runConfig.RemoteAuthConfig != nil {
		remoteOAuthConfig = &RemoteOAuthConfig{
			Issuer:       runConfig.RemoteAuthConfig.Issuer,
			AuthorizeURL: runConfig.RemoteAuthConfig.AuthorizeURL,
			TokenURL:     runConfig.RemoteAuthConfig.TokenURL,
			ClientID:     runConfig.RemoteAuthConfig.ClientID,
			Scopes:       runConfig.RemoteAuthConfig.Scopes,
			OAuthParams:  runConfig.RemoteAuthConfig.OAuthParams,
			CallbackPort: runConfig.RemoteAuthConfig.CallbackPort,
		}
		headers = runConfig.RemoteAuthConfig.Headers
	}

	authzConfigPath := ""

	return &CreateRequest{
		UpdateRequest: UpdateRequest{
			Image:             runConfig.Image,
			Host:              runConfig.Host,
			CmdArguments:      runConfig.CmdArgs,
			TargetPort:        runConfig.TargetPort,
			EnvVars:           runConfig.EnvVars,
			Secrets:           secretParams,
			Volumes:           runConfig.Volumes,
			Transport:         string(runConfig.Transport),
			AuthzConfig:       authzConfigPath,
			OIDC:              oidcConfig,
			PermissionProfile: runConfig.PermissionProfile,
			ProxyMode:         string(runConfig.ProxyMode),
			NetworkIsolation:  runConfig.IsolateNetwork,
			ToolsFilter:       runConfig.ToolsFilter,
			Group:             runConfig.Group,
			URL:               runConfig.RemoteURL,
			OAuthConfig:       remoteOAuthConfig,
			Headers:           headers,
		},
		Name: runConfig.Name,
	}
}
