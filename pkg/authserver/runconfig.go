// Package authserver provides a centralized OAuth Authorization Server
// implementation using ory/fosite for issuing JWTs to clients.
package authserver

import (
	"fmt"
	"os"
	"time"
)

// Environment variable names for upstream configuration
const (
	// UpstreamClientSecretEnvVar is the environment variable name for the upstream OAuth client secret.
	// This corresponds to the "client_secret" field in the upstream configuration.
	//nolint:gosec // G101: This is an environment variable name, not a credential
	UpstreamClientSecretEnvVar = "TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET"
)

// RunConfig is the serializable configuration for the embedded OAuth authorization server.
// This is embedded in runner.RunConfig and serialized to JSON for proxyrunner.
// It contains only data that can be safely serialized (no runtime objects like private keys).
type RunConfig struct {
	// Enabled indicates whether the embedded authorization server is enabled.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	Issuer string `json:"issuer" yaml:"issuer"`

	// SigningKeyPath is the path to the RSA private key file used for signing JWT tokens.
	SigningKeyPath string `json:"signing_key_path" yaml:"signing_key_path"`

	// HMACSecretPath is the path to a file containing the HMAC secret (32+ bytes).
	// Required for multi-replica deployments to ensure consistent token validation.
	// If not set, a random secret is generated (single-instance only).
	HMACSecretPath string `json:"hmac_secret_path,omitempty" yaml:"hmac_secret_path,omitempty"`

	// AccessTokenLifespan is the duration that access tokens are valid.
	// If not set, defaults to 1 hour.
	AccessTokenLifespan time.Duration `json:"access_token_lifespan,omitempty" yaml:"access_token_lifespan,omitempty"`

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// If not set, defaults to 7 days.
	RefreshTokenLifespan time.Duration `json:"refresh_token_lifespan,omitempty" yaml:"refresh_token_lifespan,omitempty"`

	// Upstream contains configuration for connecting to an upstream IDP.
	// If nil, no upstream IDP is configured and the server operates in standalone mode.
	Upstream *RunUpstreamConfig `json:"upstream,omitempty" yaml:"upstream,omitempty"`

	// Clients is the list of pre-registered OAuth clients.
	Clients []RunClientConfig `json:"clients,omitempty" yaml:"clients,omitempty"`
}

// RunUpstreamConfig contains serializable configuration for connecting to an upstream IDP.
type RunUpstreamConfig struct {
	// Issuer is the URL of the upstream IDP (e.g., https://accounts.google.com).
	Issuer string `json:"issuer" yaml:"issuer"`

	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	// Either ClientSecret or ClientSecretFile must be set.
	ClientSecret string `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`

	// ClientSecretFile is the path to a file containing the OAuth client secret.
	// Either ClientSecret or ClientSecretFile must be set.
	ClientSecretFile string `json:"client_secret_file,omitempty" yaml:"client_secret_file,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// RunClientConfig defines a pre-registered OAuth client in serializable form.
type RunClientConfig struct {
	// ID is the unique identifier for this client.
	ID string `json:"id" yaml:"id"`

	// Secret is the client secret. Required for confidential clients.
	// For public clients, this should be empty.
	Secret string `json:"secret,omitempty" yaml:"secret,omitempty"`

	// RedirectURIs is the list of allowed redirect URIs for this client.
	RedirectURIs []string `json:"redirect_uris" yaml:"redirect_uris"`

	// Public indicates whether this is a public client (e.g., native app, SPA).
	// Public clients do not have a secret.
	Public bool `json:"public" yaml:"public"`
}

// Validate checks that the RunConfig is valid.
// If Enabled is false, no validation is performed.
func (c *RunConfig) Validate() error {
	if !c.Enabled {
		return nil
	}

	if c.Issuer == "" {
		return fmt.Errorf("issuer is required when auth server is enabled")
	}

	if c.SigningKeyPath == "" {
		return fmt.Errorf("signing_key_path is required when auth server is enabled")
	}

	if c.Upstream != nil {
		if err := c.Upstream.Validate(); err != nil {
			return fmt.Errorf("upstream config: %w", err)
		}
	}

	for i, client := range c.Clients {
		if err := client.Validate(); err != nil {
			return fmt.Errorf("client %d: %w", i, err)
		}
	}

	return nil
}

// Validate checks that the RunUpstreamConfig is valid.
func (c *RunUpstreamConfig) Validate() error {
	if c.Issuer == "" {
		return fmt.Errorf("upstream issuer is required")
	}

	if c.ClientID == "" {
		return fmt.Errorf("upstream client_id is required")
	}

	// Check if client secret is available from any source:
	// 1. Direct config value (ClientSecret)
	// 2. File path (ClientSecretFile)
	// 3. Environment variable (UpstreamClientSecretEnvVar)
	hasEnvSecret := os.Getenv(UpstreamClientSecretEnvVar) != ""
	if c.ClientSecret == "" && c.ClientSecretFile == "" && !hasEnvSecret {
		return fmt.Errorf("either upstream client_secret, client_secret_file, or %s environment variable is required",
			UpstreamClientSecretEnvVar)
	}

	// Only check for conflicts between config fields (not env var, since env var is a fallback)
	if c.ClientSecret != "" && c.ClientSecretFile != "" {
		return fmt.Errorf("only one of upstream client_secret or client_secret_file can be set")
	}

	return nil
}

// Validate checks that the RunClientConfig is valid.
func (c *RunClientConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("client id is required")
	}

	if len(c.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirect_uri is required")
	}

	if !c.Public && c.Secret == "" {
		return fmt.Errorf("secret is required for confidential clients")
	}

	return nil
}
