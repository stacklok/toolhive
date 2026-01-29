// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
)

// TokenPersister is a callback function that persists OAuth refresh tokens.
// It is called whenever tokens are refreshed. Only the refresh token is persisted
// since the access token can be regenerated from it.
type TokenPersister func(refreshToken string, expiry time.Time) error

// ClientCredentialsPersister is called when DCR client credentials need to be persisted.
// This is used to store client_id and client_secret obtained during Dynamic Client Registration.
type ClientCredentialsPersister func(clientID, clientSecret string) error

// PersistingTokenSource wraps an oauth2.TokenSource and persists tokens
// whenever they are refreshed. This enables session restoration across
// workload restarts without requiring a new browser-based OAuth flow.
type PersistingTokenSource struct {
	source    oauth2.TokenSource
	persister TokenPersister

	mu        sync.Mutex
	lastToken *oauth2.Token
}

// NewPersistingTokenSource creates a new PersistingTokenSource that wraps
// the given token source and calls the persister function whenever tokens
// are refreshed.
func NewPersistingTokenSource(source oauth2.TokenSource, persister TokenPersister) *PersistingTokenSource {
	return &PersistingTokenSource{
		source:    source,
		persister: persister,
	}
}

// Token returns a valid token, refreshing it if necessary.
// If the token was refreshed, it will be persisted using the configured persister.
func (p *PersistingTokenSource) Token() (*oauth2.Token, error) {
	token, err := p.source.Token()
	if err != nil {
		return nil, err
	}

	// Check if the refresh token changed - only persist when it actually differs
	// Refresh tokens are long-lived and usually don't change on every access token refresh
	p.mu.Lock()
	defer p.mu.Unlock()

	if token.RefreshToken != "" && p.persister != nil &&
		(p.lastToken == nil || token.RefreshToken != p.lastToken.RefreshToken) {
		// Refresh token changed, persist it
		if err := p.persister(token.RefreshToken, token.Expiry); err != nil {
			// Log the error but don't fail the token retrieval
			logger.Warnf("Failed to persist refreshed OAuth token: %v", err)
		} else {
			logger.Debugf("Successfully persisted refreshed OAuth token")
		}
		p.lastToken = token
	}

	return token, nil
}

// CreateTokenSourceFromCached creates an oauth2.TokenSource from a cached refresh token.
// The returned token source will immediately refresh to get a new access token,
// then automatically refresh when it expires.
func CreateTokenSourceFromCached(
	config *oauth2.Config,
	refreshToken string,
	expiry time.Time,
) oauth2.TokenSource {
	// Create a token with only the refresh token.
	// The access token is intentionally empty - ReuseTokenSource will detect
	// that the token is expired (since Expiry is in the past or AccessToken is empty)
	// and trigger a refresh using the refresh token.
	token := &oauth2.Token{
		AccessToken:  "", // Empty - will trigger immediate refresh
		RefreshToken: refreshToken,
		Expiry:       expiry,
		TokenType:    "Bearer",
	}

	// ReuseTokenSource will automatically refresh the token when it expires
	return oauth2.ReuseTokenSource(token, config.TokenSource(context.TODO(), token))
}
