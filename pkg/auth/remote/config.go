package remote

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/registry"
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
	Headers []*registry.Header `json:"headers,omitempty" yaml:"headers,omitempty"`

	// Environment variables for the client
	EnvVars []*registry.EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

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
			ClientID         string             `json:"ClientID,omitempty"`
			ClientSecret     string             `json:"ClientSecret,omitempty"`
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
