// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication support for MCP server registries.
package auth

import (
	"context"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// TokenSource provides authentication tokens for registry HTTP requests.
type TokenSource interface {
	// Token returns a valid access token string, or empty string if no auth.
	// Implementations should handle token refresh transparently.
	Token(ctx context.Context) (string, error)
}

// NewTokenSource creates a TokenSource from registry OAuth configuration.
// Returns nil, nil if oauth config is nil (no auth required).
// The secrets provider may be nil if secret storage is not available.
func NewTokenSource(cfg *config.RegistryOAuthConfig, secretsProvider secrets.Provider) (TokenSource, error) {
	if cfg == nil {
		return nil, nil
	}

	return &oauthTokenSource{
		oauthCfg:        cfg,
		secretsProvider: secretsProvider,
	}, nil
}
