// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
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
	updateFn, err := auth.ConfigureOAuth(context.Background(), issuer, clientID, audience, scopes)
	if err != nil {
		return fmt.Errorf("configuring OAuth: %w", err)
	}
	return c.provider.UpdateConfig(updateFn)
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
