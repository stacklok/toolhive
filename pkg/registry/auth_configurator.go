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

// AuthConfigurator provides operations for managing registry authentication configuration.
type AuthConfigurator interface {
	// SetOAuthAuth configures OAuth/OIDC authentication for the registry.
	// Validates the OIDC issuer before saving configuration.
	SetOAuthAuth(issuer, clientID, audience string, scopes []string, usePKCE bool) error

	// UnsetAuth removes registry authentication configuration.
	UnsetAuth() error

	// GetAuthInfo returns the current auth type and whether tokens are cached.
	GetAuthInfo() (authType string, hasCachedTokens bool)
}

// DefaultAuthConfigurator is the default implementation of AuthConfigurator.
type DefaultAuthConfigurator struct {
	provider config.Provider
}

// NewAuthConfigurator creates a new registry auth configurator.
func NewAuthConfigurator() AuthConfigurator {
	return &DefaultAuthConfigurator{
		provider: config.NewDefaultProvider(),
	}
}

// SetOAuthAuth configures OAuth/OIDC authentication for the registry.
func (c *DefaultAuthConfigurator) SetOAuthAuth(issuer, clientID, audience string, scopes []string, usePKCE bool) error {
	// Validate OIDC issuer by attempting discovery
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := oauth.DiscoverOIDCEndpoints(ctx, issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery failed for issuer %s: %w\n"+
			"Please verify the issuer URL is correct and accessible", issuer, err)
	}

	return c.provider.UpdateConfig(func(cfg *config.Config) {
		cfg.RegistryAuth = config.RegistryAuth{
			Type: "oauth",
			OAuth: &config.RegistryOAuthConfig{
				Issuer:       issuer,
				ClientID:     clientID,
				Scopes:       scopes,
				Audience:     audience,
				UsePKCE:      usePKCE,
				CallbackPort: remote.DefaultCallbackPort,
			},
		}
	})
}

// UnsetAuth removes registry authentication configuration.
func (c *DefaultAuthConfigurator) UnsetAuth() error {
	return c.provider.UpdateConfig(func(cfg *config.Config) {
		cfg.RegistryAuth = config.RegistryAuth{}
	})
}

// GetAuthInfo returns the current auth type and whether tokens are cached.
func (c *DefaultAuthConfigurator) GetAuthInfo() (string, bool) {
	cfg := c.provider.GetConfig()
	if cfg.RegistryAuth.Type == "" {
		return "", false
	}

	hasCachedTokens := cfg.RegistryAuth.OAuth != nil &&
		cfg.RegistryAuth.OAuth.CachedRefreshTokenRef != ""

	return cfg.RegistryAuth.Type, hasCachedTokens
}
