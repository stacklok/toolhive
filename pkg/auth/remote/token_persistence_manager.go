// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/secrets"
)

// TokenPersistenceManager retrieves cached refresh tokens from a secrets provider
// and creates an oauth2.TokenSource from them.
// The oauth2.Config is intentionally not held here — callers build it themselves
// because endpoint discovery and credential resolution differ between use sites.
type TokenPersistenceManager struct {
	secretsProvider secrets.Provider
}

// NewTokenPersistenceManager creates a TokenPersistenceManager backed by the
// given secrets provider. Returns an error if provider is nil.
func NewTokenPersistenceManager(provider secrets.Provider) (*TokenPersistenceManager, error) {
	if provider == nil {
		return nil, fmt.Errorf("secrets provider is required")
	}
	return &TokenPersistenceManager{secretsProvider: provider}, nil
}

// FetchRefreshToken retrieves the refresh token stored under tokenKey from the
// secrets provider. Returns an error if the provider returns an error or the
// token is empty (not yet cached).
//
// Use this when you need to verify a cached token exists before performing
// expensive operations (e.g. network-based OIDC discovery), then create the
// token source separately via CreateTokenSourceFromCached.
func (m *TokenPersistenceManager) FetchRefreshToken(ctx context.Context, tokenKey string) (string, error) {
	token, err := m.secretsProvider.GetSecret(ctx, tokenKey)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve cached refresh token: %w", err)
	}
	if token == "" {
		return "", fmt.Errorf("no cached refresh token found")
	}
	return token, nil
}

// RestoreFromCache retrieves the refresh token stored under tokenKey from the
// secrets provider and creates a token source using the supplied oauth2.Config,
// expiry, and resource indicator.
//
// Use this when the oauth2.Config is already built before calling (e.g. config
// comes from static values or already-completed discovery). If building the
// config requires expensive operations like OIDC discovery, use FetchRefreshToken
// first to confirm a cached token exists before incurring that cost.
//
// It does NOT call Token() to verify, and does NOT wrap with a TokenPersister.
// Those are caller responsibilities.
func (m *TokenPersistenceManager) RestoreFromCache(
	ctx context.Context,
	tokenKey string,
	oauth2Cfg *oauth2.Config,
	expiry time.Time,
	resource string,
) (oauth2.TokenSource, error) {
	refreshToken, err := m.FetchRefreshToken(ctx, tokenKey)
	if err != nil {
		return nil, err
	}
	return CreateTokenSourceFromCached(oauth2Cfg, refreshToken, expiry, resource), nil
}
