// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

// Auth status constants for API responses.
const (
	// AuthStatusNone indicates no registry auth is configured.
	AuthStatusNone = "none"
	// AuthStatusConfigured indicates auth is configured but no cached tokens exist.
	AuthStatusConfigured = "configured"
	// AuthStatusAuthenticated indicates auth is configured with cached tokens from a prior login.
	AuthStatusAuthenticated = "authenticated"
)

// OAuthPublicConfig holds the non-secret OAuth configuration fields
// suitable for returning in API responses.
type OAuthPublicConfig struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Audience string   `json:"audience,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}

// AuthManager provides operations for managing registry authentication configuration.
type AuthManager interface {
	// SetOAuthAuth configures OIDC authentication for the registry.
	// Validates the OIDC issuer before saving configuration.
	SetOAuthAuth(ctx context.Context, issuer, clientID, audience string, scopes []string) error

	// UnsetAuth removes registry authentication configuration.
	UnsetAuth() error

	// GetAuthInfo returns the current auth type and whether tokens are cached.
	GetAuthInfo() (authType string, hasCachedTokens bool)

	// GetAuthStatus returns the auth status and auth type for API responses.
	// Status is one of AuthStatusNone, AuthStatusConfigured, or AuthStatusAuthenticated.
	// AuthType is "oauth" or empty string when no auth is configured.
	GetAuthStatus() (status, authType string)

	// GetOAuthPublicConfig returns the non-secret OAuth configuration,
	// or nil if no OAuth auth is configured.
	GetOAuthPublicConfig() *OAuthPublicConfig
}

// DefaultAuthManager is the default implementation of AuthManager.
type DefaultAuthManager struct {
	provider config.Provider
}

// NewAuthManager creates a new registry auth manager using the given config provider.
func NewAuthManager(provider config.Provider) AuthManager {
	return &DefaultAuthManager{
		provider: provider,
	}
}

// SetOAuthAuth configures OIDC authentication for the registry.
// PKCE (S256) is always enforced and not configurable.
func (c *DefaultAuthManager) SetOAuthAuth(ctx context.Context, issuer, clientID, audience string, scopes []string) error {
	updateFn, err := auth.ConfigureOAuth(ctx, issuer, clientID, audience, scopes)
	if err != nil {
		return fmt.Errorf("configuring OAuth: %w", err)
	}
	return c.provider.UpdateConfig(updateFn)
}

// UnsetAuth removes registry authentication configuration.
func (c *DefaultAuthManager) UnsetAuth() error {
	return c.provider.UpdateConfig(func(cfg *config.Config) error {
		cfg.RegistryAuth = config.RegistryAuth{}
		return nil
	})
}

// GetAuthInfo returns the current auth type and whether tokens are cached.
func (c *DefaultAuthManager) GetAuthInfo() (string, bool) {
	cfg := c.provider.GetConfig()
	if cfg.RegistryAuth.Type == "" {
		return "", false
	}

	hasCachedTokens := cfg.RegistryAuth.OAuth != nil &&
		cfg.RegistryAuth.OAuth.CachedRefreshTokenRef != ""

	return cfg.RegistryAuth.Type, hasCachedTokens
}

// GetAuthStatus returns the auth status and auth type for API responses.
func (c *DefaultAuthManager) GetAuthStatus() (string, string) {
	authType, hasCachedTokens := c.GetAuthInfo()
	if authType == "" {
		return AuthStatusNone, ""
	}
	if hasCachedTokens {
		return AuthStatusAuthenticated, authType
	}
	return AuthStatusConfigured, authType
}

// GetOAuthPublicConfig returns the non-secret OAuth configuration,
// or nil if no OAuth auth is configured.
func (c *DefaultAuthManager) GetOAuthPublicConfig() *OAuthPublicConfig {
	cfg := c.provider.GetConfig()
	if cfg.RegistryAuth.Type != config.RegistryAuthTypeOAuth || cfg.RegistryAuth.OAuth == nil {
		return nil
	}
	return &OAuthPublicConfig{
		Issuer:   cfg.RegistryAuth.OAuth.Issuer,
		ClientID: cfg.RegistryAuth.OAuth.ClientID,
		Audience: cfg.RegistryAuth.OAuth.Audience,
		Scopes:   cfg.RegistryAuth.OAuth.Scopes,
	}
}
