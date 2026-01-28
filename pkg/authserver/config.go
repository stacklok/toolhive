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
)

// UpstreamConfig wraps an upstream IDP configuration with identifying metadata.
type UpstreamConfig struct {
	// Name uniquely identifies this upstream.
	// Used for routing decisions and session binding in multi-upstream scenarios.
	// If empty when only one upstream is configured, defaults to "default".
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Config contains the OAuth 2.0 provider configuration.
	Config *upstream.OAuth2Config `json:"config" yaml:"config"`
}

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

	// Upstreams contains configurations for connecting to upstream IDPs.
	// At least one upstream is required - the server delegates authentication to the upstream IDP.
	// Currently only a single upstream is supported.
	Upstreams []UpstreamConfig
}

// GetUpstream returns the primary upstream configuration.
// For current single-upstream deployments, this returns the only configured upstream.
// Returns nil if no upstreams are configured (call Validate first).
func (c *Config) GetUpstream() *upstream.OAuth2Config {
	if len(c.Upstreams) == 0 {
		return nil
	}
	return c.Upstreams[0].Config
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

	if err := c.validateUpstreams(); err != nil {
		return err
	}

	logger.Debugw("authserver config validation passed",
		"issuer", c.Issuer,
		"upstreamCount", len(c.Upstreams),
	)
	return nil
}

// validateUpstreams validates the upstream configurations.
func (c *Config) validateUpstreams() error {
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	if len(c.Upstreams) > 1 {
		return fmt.Errorf("multiple upstreams not yet supported (found %d)", len(c.Upstreams))
	}

	// Track names for uniqueness checking
	seenNames := make(map[string]bool)

	for i := range c.Upstreams {
		up := &c.Upstreams[i]

		// Default empty name to "default"
		if up.Name == "" {
			up.Name = "default"
		}

		// Check for duplicate names
		if seenNames[up.Name] {
			return fmt.Errorf("duplicate upstream name: %q", up.Name)
		}
		seenNames[up.Name] = true

		// Validate the upstream config
		if up.Config == nil {
			return fmt.Errorf("upstream %q: config is required", up.Name)
		}
		if err := up.Config.Validate(); err != nil {
			return fmt.Errorf("upstream %q: %w", up.Name, err)
		}
	}

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
