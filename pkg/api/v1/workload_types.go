// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"fmt"
	"net/http"

	httpval "github.com/stacklok/toolhive-core/validation/http"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
)

// workloadListResponse represents the response for listing workloads
//
//	@Description	Response containing a list of workloads
type workloadListResponse struct {
	// List of container information for each workload
	Workloads []core.Workload `json:"workloads"`
}

// workloadStatusResponse represents the response for getting workload status
//
//	@Description	Response containing workload status information
type workloadStatusResponse struct {
	// Current status of the workload
	Status runtime.WorkloadStatus `json:"status"`
}

// updateRequest represents the request to update an existing workload
//
//	@Description	Request to update an existing workload (name cannot be changed)
type updateRequest struct {
	// Docker image to use
	Image string `json:"image"`
	// Host to bind to
	Host string `json:"host"`
	// Command arguments to pass to the container
	CmdArguments []string `json:"cmd_arguments"`
	// Port to expose from the container
	TargetPort int `json:"target_port"`
	// Port for the HTTP proxy to listen on
	ProxyPort int `json:"proxy_port"`
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
	// Whether to trust X-Forwarded-* headers from reverse proxies
	TrustProxyHeaders bool `json:"trust_proxy_headers"`
	// Tools filter
	ToolsFilter []string `json:"tools"`
	// Tools override
	ToolsOverride map[string]toolOverride `json:"tools_override"`
	// Group name this workload belongs to
	Group string `json:"group,omitempty"`

	// Remote server specific fields
	URL         string             `json:"url,omitempty"`
	OAuthConfig remoteOAuthConfig  `json:"oauth_config,omitempty"`
	Headers     []*registry.Header `json:"headers,omitempty"`

	// HeaderForward configures headers to inject into requests to remote MCP servers.
	// Use this to add custom headers like X-Tenant-ID or correlation IDs.
	HeaderForward *headerForwardConfig `json:"header_forward,omitempty"`
}

// toolOverride represents a tool override
//
//	@Description	Tool override
type toolOverride struct {
	// Name of the tool
	Name string `json:"name,omitempty"`
	// Description of the tool
	Description string `json:"description,omitempty"`
}

// headerForwardConfig represents header forward configuration for API requests/responses
//
//	@Description	Configuration for injecting headers into requests to remote MCP servers
type headerForwardConfig struct {
	// AddPlaintextHeaders contains literal header values to inject.
	// WARNING: These values are stored and transmitted in plaintext.
	// Use AddHeadersFromSecret for sensitive data like API keys.
	AddPlaintextHeaders map[string]string `json:"add_plaintext_headers,omitempty"`

	// AddHeadersFromSecret maps header names to secret names in ToolHive's secrets manager.
	// Key: HTTP header name, Value: secret name in the secrets manager
	AddHeadersFromSecret map[string]string `json:"add_headers_from_secret,omitempty"`
}

// remoteOAuthConfig represents OAuth configuration for remote servers
//
//	@Description	OAuth configuration for remote server authentication
//
// @name remoteOAuthConfig
type remoteOAuthConfig struct {
	// OAuth/OIDC issuer URL (e.g., https://accounts.google.com)
	Issuer string `json:"issuer,omitempty"`
	// OAuth authorization endpoint URL (alternative to issuer for non-OIDC OAuth)
	AuthorizeURL string `json:"authorize_url,omitempty"`
	// OAuth token endpoint URL (alternative to issuer for non-OIDC OAuth)
	TokenURL string `json:"token_url,omitempty"`
	// OAuth client ID for authentication
	ClientID     string                   `json:"client_id,omitempty"`
	ClientSecret *secrets.SecretParameter `json:"client_secret,omitempty"`
	// Bearer token for authentication (alternative to OAuth)
	BearerToken *secrets.SecretParameter `json:"bearer_token,omitempty"`

	// OAuth scopes to request
	Scopes []string `json:"scopes,omitempty"`
	// Whether to use PKCE for the OAuth flow
	UsePKCE bool `json:"use_pkce,omitempty"`
	// Additional OAuth parameters for server-specific customization
	OAuthParams map[string]string `json:"oauth_params,omitempty"`
	// Specific port for OAuth callback server
	CallbackPort int `json:"callback_port,omitempty"`
	// Whether to skip opening browser for OAuth flow (defaults to false)
	SkipBrowser bool `json:"skip_browser,omitempty"`
	// OAuth 2.0 resource indicator (RFC 8707)
	Resource string `json:"resource,omitempty"`
}

// createRequest represents the request to create a new workload
//
//	@Description	Request to create a new workload
type createRequest struct {
	updateRequest
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
	ClientSecret string `json:"client_secret"` //nolint:gosec // G117
	// OAuth scopes to advertise in well-known endpoint (RFC 9728)
	Scopes []string `json:"scopes,omitempty"`
}

// createWorkloadResponse represents the response for workload creation
//
//	@Description	Response after successfully creating a workload
type createWorkloadResponse struct {
	// Name of the created workload
	Name string `json:"name"`
	// Port the workload is listening on
	Port int `json:"port"`
}

// bulkOperationRequest represents a request for bulk operations on workloads
type bulkOperationRequest struct {
	// Names of the workloads to operate on
	Names []string `json:"names"`
	// Group name to operate on (mutually exclusive with names)
	Group string `json:"group,omitempty"`
}

// validateBulkOperationRequest validates the bulk operation request
func validateBulkOperationRequest(req bulkOperationRequest) error {
	if len(req.Names) > 0 && req.Group != "" {
		return fmt.Errorf("cannot specify both names and group")
	}
	if len(req.Names) == 0 && req.Group == "" {
		return fmt.Errorf("must specify either names or group")
	}
	return nil
}

// runConfigToCreateRequest converts a RunConfig to createRequest for API responses
func runConfigToCreateRequest(runConfig *runner.RunConfig) *createRequest {
	if runConfig == nil {
		return nil
	}

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
			Scopes:           runConfig.OIDCConfig.Scopes,
		}
	}

	// Get remote OAuth config from RunConfig
	var oAuthConfig remoteOAuthConfig
	var headers []*registry.Header
	if runConfig.RemoteAuthConfig != nil {
		// Parse ClientSecret from CLI format to SecretParameter (for details API)
		var clientSecretParam *secrets.SecretParameter
		if runConfig.RemoteAuthConfig.ClientSecret != "" {
			// Parse the CLI format: "<name>,target=<target>"
			if secretParam, err := secrets.ParseSecretParameter(runConfig.RemoteAuthConfig.ClientSecret); err == nil {
				clientSecretParam = &secretParam
			}
			// Ignore invalid secrets rather than failing the entire conversion
		}

		// Parse BearerToken from CLI format to SecretParameter (for details API)
		var bearerTokenParam *secrets.SecretParameter
		if runConfig.RemoteAuthConfig.BearerToken != "" {
			// Parse the CLI format: "<name>,target=<target>"
			if secretParam, err := secrets.ParseSecretParameter(runConfig.RemoteAuthConfig.BearerToken); err == nil {
				bearerTokenParam = &secretParam
			}
			// Ignore invalid secrets rather than failing the entire conversion
		}

		oAuthConfig = remoteOAuthConfig{
			Issuer:       runConfig.RemoteAuthConfig.Issuer,
			AuthorizeURL: runConfig.RemoteAuthConfig.AuthorizeURL,
			TokenURL:     runConfig.RemoteAuthConfig.TokenURL,
			ClientID:     runConfig.RemoteAuthConfig.ClientID,
			ClientSecret: clientSecretParam,
			BearerToken:  bearerTokenParam,
			Scopes:       runConfig.RemoteAuthConfig.Scopes,
			UsePKCE:      runConfig.RemoteAuthConfig.UsePKCE,
			OAuthParams:  runConfig.RemoteAuthConfig.OAuthParams,
			CallbackPort: runConfig.RemoteAuthConfig.CallbackPort,
			SkipBrowser:  runConfig.RemoteAuthConfig.SkipBrowser,
			Resource:     runConfig.RemoteAuthConfig.Resource,
		}
		headers = runConfig.RemoteAuthConfig.Headers
	}

	authzConfigPath := ""

	// Convert ToolsOverride from runner.ToolOverride to API toolOverride
	var toolsOverride map[string]toolOverride
	if runConfig.ToolsOverride != nil {
		toolsOverride = make(map[string]toolOverride, len(runConfig.ToolsOverride))
		for key, override := range runConfig.ToolsOverride {
			toolsOverride[key] = toolOverride{
				Name:        override.Name,
				Description: override.Description,
			}
		}
	}

	// Convert HeaderForward from RunConfig
	var headerForward *headerForwardConfig
	if runConfig.HeaderForward != nil {
		headerForward = &headerForwardConfig{
			AddPlaintextHeaders:  runConfig.HeaderForward.AddPlaintextHeaders,
			AddHeadersFromSecret: runConfig.HeaderForward.AddHeadersFromSecret,
		}
	}

	return &createRequest{
		updateRequest: updateRequest{
			Image:             runConfig.Image,
			Host:              runConfig.Host,
			CmdArguments:      runConfig.CmdArgs,
			TargetPort:        runConfig.TargetPort,
			ProxyPort:         runConfig.Port,
			EnvVars:           runConfig.EnvVars,
			Secrets:           secretParams,
			Volumes:           runConfig.Volumes,
			Transport:         string(runConfig.Transport),
			AuthzConfig:       authzConfigPath,
			OIDC:              oidcConfig,
			PermissionProfile: runConfig.PermissionProfile,
			ProxyMode:         string(runConfig.ProxyMode),
			NetworkIsolation:  runConfig.IsolateNetwork,
			TrustProxyHeaders: runConfig.TrustProxyHeaders,
			ToolsFilter:       runConfig.ToolsFilter,
			ToolsOverride:     toolsOverride,
			Group:             runConfig.Group,
			URL:               runConfig.RemoteURL,
			OAuthConfig:       oAuthConfig,
			Headers:           headers,
			HeaderForward:     headerForward,
		},
		Name: runConfig.Name,
	}
}

// validateHeaderForwardConfig validates the header forward configuration.
// Returns an error if any header name is restricted/invalid or any value contains control characters.
func validateHeaderForwardConfig(config *headerForwardConfig) error {
	if config == nil {
		return nil
	}

	// Validate plaintext headers (both name and value)
	for name, value := range config.AddPlaintextHeaders {
		if err := validateHeaderName(name); err != nil {
			return err
		}
		// Validate value for CRLF injection and control characters per RFC 7230
		if value != "" {
			if err := httpval.ValidateHeaderValue(value); err != nil {
				return fmt.Errorf("invalid header value for %q: %w", name, err)
			}
		}
	}

	// Validate secret-backed header names (values are validated at resolution time)
	for name := range config.AddHeadersFromSecret {
		if err := validateHeaderName(name); err != nil {
			return err
		}
	}

	return nil
}

// validateHeaderName checks if a header name is valid per RFC 7230 and not restricted.
func validateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("header name cannot be empty")
	}

	// Validate header name format per RFC 7230
	if err := httpval.ValidateHeaderName(name); err != nil {
		return fmt.Errorf("invalid header name %q: %w", name, err)
	}

	// Check for restricted headers using canonical form
	canonical := http.CanonicalHeaderKey(name)
	if middleware.RestrictedHeaders[canonical] {
		return fmt.Errorf("header %q is restricted and cannot be configured for forwarding", name)
	}

	return nil
}
