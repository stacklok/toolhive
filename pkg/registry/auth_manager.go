// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
)

// AuthManager provides operations for managing registry authentication configuration.
type AuthManager interface {
	// SetOAuthAuth configures OAuth/OIDC authentication for the registry.
	// Validates the OIDC issuer before saving configuration.
	SetOAuthAuth(issuer, clientID, audience string, scopes []string) error

	// UnsetAuth removes registry authentication configuration.
	UnsetAuth() error

	// GetAuthInfo returns the current auth type and whether tokens are cached.
	GetAuthInfo() (authType string, hasCachedTokens bool)
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

// SetOAuthAuth configures OAuth/OIDC authentication for the registry.
// PKCE (S256) is always enforced and not configurable.
func (c *DefaultAuthManager) SetOAuthAuth(issuer, clientID, audience string, scopes []string) error {
	// Validate OIDC issuer by attempting discovery
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := oauth.DiscoverOIDCEndpoints(ctx, issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery failed for issuer %s: %w", issuer, err)
	}

	// Default to openid + offline_access if no scopes provided.
	// offline_access is required to receive a refresh token from the provider.
	if len(scopes) == 0 {
		scopes = []string{"openid", "offline_access"}
	}

	return c.provider.UpdateConfig(func(cfg *config.Config) {
		cfg.RegistryAuth = config.RegistryAuth{
			Type: config.RegistryAuthTypeOAuth,
			OAuth: &config.RegistryOAuthConfig{
				Issuer:       issuer,
				ClientID:     clientID,
				Scopes:       scopes,
				Audience:     audience,
				CallbackPort: remote.DefaultCallbackPort,
			},
		}
	})
}

// UnsetAuth removes registry authentication configuration.
func (c *DefaultAuthManager) UnsetAuth() error {
	return c.provider.UpdateConfig(func(cfg *config.Config) {
		cfg.RegistryAuth = config.RegistryAuth{}
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
