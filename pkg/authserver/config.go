// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides configuration and validation for the OAuth authorization server.
package authserver

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"time"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauth"
)

// Config is the pure configuration for the OAuth authorization server.
// All values must be fully resolved (no file paths, no env vars).
// This is the interface that consumers should use to configure the server.
type Config struct {
	// Issuer is the issuer identifier for this authorization server.
	// This will be included in the "iss" claim of issued tokens.
	Issuer string

	// KeyProvider provides signing keys for JWT operations.
	// Supports key rotation by returning multiple public keys for JWKS.
	// If nil, an ephemeral key will be auto-generated (development only).
	//
	// Production: Use keys.NewFileProvider() or keys.NewProviderFromConfig()
	// Testing: Use a mock or keys.NewGeneratingProvider()
	KeyProvider keys.KeyProvider

	// HMACSecrets contains the symmetric secrets used for signing authorization codes
	// and refresh tokens (opaque tokens). Unlike the asymmetric SigningKey which
	// signs JWTs for distributed verification, these secrets are used internally
	// by the authorization server only.
	// Current secret must be at least 32 bytes and cryptographically random.
	// Must be consistent across all replicas in multi-instance deployments.
	// Supports secret rotation via the Rotated field.
	HMACSecrets *servercrypto.HMACSecrets

	// AccessTokenLifespan is the duration that access tokens are valid.
	// If zero, defaults to 1 hour.
	AccessTokenLifespan time.Duration

	// RefreshTokenLifespan is the duration that refresh tokens are valid.
	// If zero, defaults to 7 days.
	RefreshTokenLifespan time.Duration

	// AuthCodeLifespan is the duration that authorization codes are valid.
	// If zero, defaults to 10 minutes.
	AuthCodeLifespan time.Duration

	// Clients is the list of pre-registered OAuth clients.
	Clients []ClientConfig

	// Upstream contains configuration for connecting to an upstream IDP.
	// This field is required - the server delegates authentication to the upstream IDP.
	Upstream *upstream.OAuth2Config
}

// ClientConfig defines a pre-registered OAuth client.
type ClientConfig struct {
	// ID is the unique identifier for this client.
	ID string `json:"id" yaml:"id"`

	// Secret is the client secret. Required for confidential clients.
	// For public clients, this should be empty.
	Secret string `json:"secret,omitempty" yaml:"secret,omitempty"`

	// RedirectURIs is the list of allowed redirect URIs for this client.
	RedirectURIs []string `json:"redirect_uris" yaml:"redirect_uris"`

	// Public indicates whether this is a public client (e.g., native app, SPA).
	// Public clients do not have a secret.
	Public bool `json:"public,omitempty" yaml:"public,omitempty"`
}

// Validate checks that the Config is valid.
func (c *Config) Validate() error {
	logger.Debugw("validating authserver config", "issuer", c.Issuer)

	if err := validateIssuerURL(c.Issuer); err != nil {
		return fmt.Errorf("issuer: %w", err)
	}

	// KeyProvider is optional - if nil, applyDefaults() will create a GeneratingProvider

	if c.HMACSecrets == nil {
		return fmt.Errorf("HMAC secrets are required")
	}
	if len(c.HMACSecrets.Current) < servercrypto.MinSecretLength {
		return fmt.Errorf("HMAC secret must be at least %d bytes", servercrypto.MinSecretLength)
	}

	for i, client := range c.Clients {
		if err := client.Validate(); err != nil {
			return fmt.Errorf("client %d: %w", i, err)
		}
	}

	if c.Upstream == nil {
		return fmt.Errorf("upstream config is required")
	}
	if err := c.Upstream.Validate(); err != nil {
		return fmt.Errorf("upstream config: %w", err)
	}

	logger.Debugw("authserver config validation passed",
		"issuer", c.Issuer,
		"clientCount", len(c.Clients),
		"hasUpstream", c.Upstream != nil,
	)
	return nil
}

// Validate checks that the ClientConfig is valid.
func (c *ClientConfig) Validate() error {
	logger.Debugw("validating client config", "clientID", c.ID, "public", c.Public)

	if c.ID == "" {
		return fmt.Errorf("client id is required")
	}

	if len(c.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirect_uri is required")
	}

	// Validate each redirect URI per RFC 6749/8252.
	// Static clients allow private-use schemes (e.g., cursor://, vscode://)
	// for native app support per RFC 8252 Section 7.1.
	for i, uri := range c.RedirectURIs {
		if err := oauth.ValidateRedirectURI(uri, oauth.RedirectURIPolicyAllowPrivateSchemes); err != nil {
			return fmt.Errorf("redirect_uri[%d]: %w", i, err)
		}
	}

	if !c.Public && c.Secret == "" {
		return fmt.Errorf("secret is required for confidential clients")
	}

	logger.Debugw("client config validated", "clientID", c.ID, "redirectURICount", len(c.RedirectURIs))
	return nil
}

// applyDefaults applies default values to the config where not set.
func (c *Config) applyDefaults() error {
	logger.Debug("applying default values to authserver config")

	if c.AccessTokenLifespan == 0 {
		c.AccessTokenLifespan = time.Hour
		logger.Debugw("applied default access token lifespan", "duration", c.AccessTokenLifespan)
	}
	if c.RefreshTokenLifespan == 0 {
		c.RefreshTokenLifespan = 24 * time.Hour * 7 // 7 days
		logger.Debugw("applied default refresh token lifespan", "duration", c.RefreshTokenLifespan)
	}
	if c.AuthCodeLifespan == 0 {
		c.AuthCodeLifespan = 10 * time.Minute
		logger.Debugw("applied default auth code lifespan", "duration", c.AuthCodeLifespan)
	}
	if c.HMACSecrets == nil {
		secret := make([]byte, servercrypto.MinSecretLength)
		if _, err := rand.Read(secret); err != nil {
			return fmt.Errorf("failed to generate HMAC secret: %w", err)
		}
		c.HMACSecrets = &servercrypto.HMACSecrets{Current: secret}
		logger.Warnw("no HMAC secrets configured, generating ephemeral secret",
			"warning", "auth codes and refresh tokens will be invalid after restart")
	}
	if c.KeyProvider == nil {
		c.KeyProvider = keys.NewGeneratingProvider(keys.DefaultAlgorithm)
		logger.Warnw("no key provider configured, using ephemeral signing key",
			"warning", "JWTs will be invalid after restart")
	}
	return nil
}

// validateIssuerURL validates that the issuer is a valid URL.
// Per OIDC Core Section 3.1.2.1 and RFC 8414 Section 2, the issuer
// MUST use the "https" scheme, except for localhost during development.
func validateIssuerURL(issuer string) error {
	if issuer == "" {
		return fmt.Errorf("issuer is required")
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme == "" {
		return fmt.Errorf("scheme is required")
	}

	if parsed.Host == "" {
		return fmt.Errorf("host is required")
	}

	// Per RFC 8414 Section 2, the issuer identifier has no query or fragment components
	if parsed.RawQuery != "" {
		return fmt.Errorf("must not contain query component")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("must not contain fragment component")
	}

	// HTTPS is required unless it's a loopback address (for development)
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" {
			return fmt.Errorf("scheme must be https (or http for localhost)")
		}
		if !networking.IsLocalhost(parsed.Host) {
			return fmt.Errorf("http scheme is only allowed for localhost, use https for %s", parsed.Hostname())
		}
	}

	// Issuer must not have trailing slash per OIDC spec
	if strings.HasSuffix(issuer, "/") {
		return fmt.Errorf("must not have trailing slash")
	}

	return nil
}
