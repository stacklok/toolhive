// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive-core/registry/types"
	httpval "github.com/stacklok/toolhive-core/validation/http"
)

// Config holds authentication configuration for remote MCP servers.
// Supports OAuth/OIDC-based authentication with automatic discovery.
type Config struct {
	ClientID         string        `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	ClientSecret     string        `json:"client_secret,omitempty" yaml:"client_secret,omitempty"` //nolint:gosec // G117
	ClientSecretFile string        `json:"client_secret_file,omitempty" yaml:"client_secret_file,omitempty"`
	Scopes           []string      `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	SkipBrowser      bool          `json:"skip_browser,omitempty" yaml:"skip_browser,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty" swaggertype:"string" example:"5m"`
	CallbackPort     int           `json:"callback_port,omitempty" yaml:"callback_port,omitempty"`
	UsePKCE          bool          `json:"use_pkce" yaml:"use_pkce"`

	// Resource is the OAuth 2.0 resource indicator (RFC 8707).
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"`

	// OAuth endpoint configuration (from registry)
	Issuer       string `json:"issuer,omitempty" yaml:"issuer,omitempty"`
	AuthorizeURL string `json:"authorize_url,omitempty" yaml:"authorize_url,omitempty"`
	TokenURL     string `json:"token_url,omitempty" yaml:"token_url,omitempty"`

	// Headers for HTTP requests
	Headers []*registry.Header `json:"headers,omitempty" yaml:"headers,omitempty"`

	// Environment variables for the client
	EnvVars []*registry.EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// OAuth parameters for server-specific customization
	OAuthParams map[string]string `json:"oauth_params,omitempty" yaml:"oauth_params,omitempty"`

	// Bearer token configuration (alternative to OAuth)
	BearerToken     string `json:"bearer_token,omitempty" yaml:"bearer_token,omitempty"` //nolint:gosec // G117
	BearerTokenFile string `json:"bearer_token_file,omitempty" yaml:"bearer_token_file,omitempty"`

	// Cached OAuth token reference for persistence across restarts.
	// The refresh token is stored securely in the secret manager, and this field
	// contains the reference to retrieve it (e.g., "OAUTH_REFRESH_TOKEN_workload").
	// This enables session restoration without requiring a new browser-based login.
	CachedRefreshTokenRef string    `json:"cached_refresh_token_ref,omitempty" yaml:"cached_refresh_token_ref,omitempty"`
	CachedTokenExpiry     time.Time `json:"cached_token_expiry,omitempty" yaml:"cached_token_expiry,omitempty"`

	// Cached DCR client credentials for persistence across restarts.
	// These are obtained during Dynamic Client Registration and needed to refresh tokens.
	// ClientID is stored as plain text since it's public information.
	CachedClientID        string `json:"cached_client_id,omitempty" yaml:"cached_client_id,omitempty"`
	CachedClientSecretRef string `json:"cached_client_secret_ref,omitempty" yaml:"cached_client_secret_ref,omitempty"`
	// ClientSecretExpiresAt indicates when the client secret expires (if provided by the DCR server).
	// A zero value means the secret does not expire.
	CachedSecretExpiry time.Time `json:"cached_secret_expiry,omitempty" yaml:"cached_secret_expiry,omitempty"`
	// RegistrationAccessToken is used to update/delete the client registration.
	// Stored as a secret reference since it's sensitive.
	CachedRegTokenRef string `json:"cached_reg_token_ref,omitempty" yaml:"cached_reg_token_ref,omitempty"`
}

// BearerTokenEnvVarName is the environment variable name used for bearer token authentication.
// The bearer token will be read from this environment variable if not provided via flag or file.
// #nosec G101 - this is an environment variable name, not a credential
const BearerTokenEnvVarName = "TOOLHIVE_REMOTE_AUTH_BEARER_TOKEN"

// UnmarshalJSON implements custom JSON unmarshaling for backward compatibility
// This handles both the old PascalCase format and the new snake_case format
func (r *Config) UnmarshalJSON(data []byte) error {
	// Parse the JSON to check which format is being used
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Check if this is the old PascalCase format by looking for old field name
	// if one old field is present, then it's the old format
	if _, isOld := raw["ClientID"]; isOld {
		// Unmarshal using old PascalCase format
		var oldFormat struct {
			ClientID         string             `json:"ClientID,omitempty"`
			ClientSecret     string             `json:"ClientSecret,omitempty"` //nolint:gosec // G117
			ClientSecretFile string             `json:"ClientSecretFile,omitempty"`
			Scopes           []string           `json:"Scopes,omitempty"`
			SkipBrowser      bool               `json:"SkipBrowser,omitempty"`
			Timeout          time.Duration      `json:"Timeout,omitempty"`
			CallbackPort     int                `json:"CallbackPort,omitempty"`
			UsePKCE          bool               `json:"UsePKCE,omitempty"`
			Issuer           string             `json:"Issuer,omitempty"`
			AuthorizeURL     string             `json:"AuthorizeURL,omitempty"`
			TokenURL         string             `json:"TokenURL,omitempty"`
			Headers          []*registry.Header `json:"Headers,omitempty"`
			EnvVars          []*registry.EnvVar `json:"EnvVars,omitempty"`
			OAuthParams      map[string]string  `json:"OAuthParams,omitempty"`
			BearerToken      string             `json:"BearerToken,omitempty"` //nolint:gosec // G117
			BearerTokenFile  string             `json:"BearerTokenFile,omitempty"`
		}

		if err := json.Unmarshal(data, &oldFormat); err != nil {
			return fmt.Errorf("failed to unmarshal Config in old format: %w", err)
		}

		// Copy from old format to new format
		r.ClientID = oldFormat.ClientID
		r.ClientSecret = oldFormat.ClientSecret
		r.ClientSecretFile = oldFormat.ClientSecretFile
		r.Scopes = oldFormat.Scopes
		r.SkipBrowser = oldFormat.SkipBrowser
		r.Timeout = oldFormat.Timeout
		r.CallbackPort = oldFormat.CallbackPort
		r.UsePKCE = oldFormat.UsePKCE
		r.Issuer = oldFormat.Issuer
		r.AuthorizeURL = oldFormat.AuthorizeURL
		r.TokenURL = oldFormat.TokenURL
		r.Headers = oldFormat.Headers
		r.EnvVars = oldFormat.EnvVars
		r.OAuthParams = oldFormat.OAuthParams
		r.BearerToken = oldFormat.BearerToken
		r.BearerTokenFile = oldFormat.BearerTokenFile
		return nil
	}

	// Use the new snake_case format
	type Alias Config
	alias := (*Alias)(r)
	return json.Unmarshal(data, alias)
}

// DefaultCallbackPort is the default port for the OAuth callback server
const DefaultCallbackPort = 8666

// HasValidCachedTokens returns true if the config has a cached token reference that can be used
// to create a TokenSource without requiring a new OAuth flow.
// Note: This only checks if a refresh token reference exists, not if the token is actually valid.
// The actual validity will be determined when the token is used.
func (c *Config) HasValidCachedTokens() bool {
	// We need at least a refresh token reference to restore the session
	return c.CachedRefreshTokenRef != ""
}

// ClearCachedTokens removes any cached OAuth token references from the config.
// Note: This does not delete the actual secret from the secret manager.
func (c *Config) ClearCachedTokens() {
	c.CachedRefreshTokenRef = ""
	c.CachedTokenExpiry = time.Time{}
}

// HasCachedClientCredentials returns true if the config has cached DCR client credentials.
func (c *Config) HasCachedClientCredentials() bool {
	return c.CachedClientID != ""
}

// ClearCachedClientCredentials removes any cached DCR client credential references from the config.
func (c *Config) ClearCachedClientCredentials() {
	c.CachedClientID = ""
	c.CachedClientSecretRef = ""
	c.CachedSecretExpiry = time.Time{}
	c.CachedRegTokenRef = ""
}

// DefaultResourceIndicator derives the resource indicator (RFC 8707) from the remote server URL.
// This function should only be called when the user has not explicitly provided a resource indicator.
// If the resource indicator cannot be derived, it returns an empty string.
func DefaultResourceIndicator(remoteServerURL string) string {
	// Normalize the remote server URL
	normalized, err := normalizeResourceURI(remoteServerURL)
	if err != nil {
		// Normalization failed - log warning and leave resource empty
		slog.Warn("Failed to normalize resource indicator from remote server URL", "url", remoteServerURL, "error", err)
		return ""
	}

	// Validate the normalized result
	if err := httpval.ValidateResourceURI(normalized); err != nil {
		// Validation failed - log warning and leave resource empty
		slog.Warn("Normalized resource indicator is invalid", "resource", normalized, "error", err)
		return ""
	}

	return normalized
}

// normalizeResourceURI normalizes a resource URI to conform to MCP specification requirements.
// This function performs the following normalizations:
// - Lowercase scheme and host
// - Strip fragments
func normalizeResourceURI(resourceURI string) (string, error) {
	if resourceURI == "" {
		return "", fmt.Errorf("resource URI cannot be empty")
	}

	// Parse the URI
	parsed, err := url.Parse(resourceURI)
	if err != nil {
		return "", fmt.Errorf("invalid resource URI: %w", err)
	}

	// Normalize: lowercase scheme and host
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)

	// Strip fragment if present (fragments are not allowed in resource indicators)
	parsed.Fragment = ""

	return parsed.String(), nil
}
