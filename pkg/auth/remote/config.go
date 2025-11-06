package remote

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry/types"
	"github.com/stacklok/toolhive/pkg/validation"
)

// Config holds authentication configuration for remote MCP servers.
// Supports OAuth/OIDC-based authentication with automatic discovery.
type Config struct {
	ClientID         string        `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	ClientSecret     string        `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`
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
	Headers []*types.Header `json:"headers,omitempty" yaml:"headers,omitempty"`

	// Environment variables for the client
	EnvVars []*types.EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// OAuth parameters for server-specific customization
	OAuthParams map[string]string `json:"oauth_params,omitempty" yaml:"oauth_params,omitempty"`
}

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
			ClientID         string            `json:"ClientID,omitempty"`
			ClientSecret     string            `json:"ClientSecret,omitempty"`
			ClientSecretFile string            `json:"ClientSecretFile,omitempty"`
			Scopes           []string          `json:"Scopes,omitempty"`
			SkipBrowser      bool              `json:"SkipBrowser,omitempty"`
			Timeout          time.Duration     `json:"Timeout,omitempty"`
			CallbackPort     int               `json:"CallbackPort,omitempty"`
			UsePKCE          bool              `json:"UsePKCE,omitempty"`
			Issuer           string            `json:"Issuer,omitempty"`
			AuthorizeURL     string            `json:"AuthorizeURL,omitempty"`
			TokenURL         string            `json:"TokenURL,omitempty"`
			Headers          []*types.Header   `json:"Headers,omitempty"`
			EnvVars          []*types.EnvVar   `json:"EnvVars,omitempty"`
			OAuthParams      map[string]string `json:"OAuthParams,omitempty"`
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
		return nil
	}

	// Use the new snake_case format
	type Alias Config
	alias := (*Alias)(r)
	return json.Unmarshal(data, alias)
}

// DefaultCallbackPort is the default port for the OAuth callback server
const DefaultCallbackPort = 8666

// DefaultResourceIndicator derives the resource indicator (RFC 8707) from the remote server URL.
// This function should only be called when the user has not explicitly provided a resource indicator.
// If the resource indicator cannot be derived, it returns an empty string.
func DefaultResourceIndicator(remoteServerURL string) string {
	// Normalize the remote server URL
	normalized, err := normalizeResourceURI(remoteServerURL)
	if err != nil {
		// Normalization failed - log warning and leave resource empty
		logger.Warnf("Failed to normalize resource indicator from remote server URL %s: %v", remoteServerURL, err)
		return ""
	}

	// Validate the normalized result
	if err := validation.ValidateResourceURI(normalized); err != nil {
		// Validation failed - log warning and leave resource empty
		logger.Warnf("Normalized resource indicator is invalid %s: %v", normalized, err)
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
