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

// TokenPersister is a callback function that persists OAuth tokens.
// It is called whenever tokens are refreshed.
type TokenPersister func(accessToken, refreshToken string, expiry time.Time) error

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

	// Check if the token was refreshed (access token changed)
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.lastToken == nil || token.AccessToken != p.lastToken.AccessToken {
		// Token was refreshed, persist it
		if p.persister != nil {
			if err := p.persister(token.AccessToken, token.RefreshToken, token.Expiry); err != nil {
				// Log the error but don't fail the token retrieval
				logger.Warnf("Failed to persist refreshed OAuth token: %v", err)
			} else {
				logger.Debugf("Successfully persisted refreshed OAuth token")
			}
		}
		p.lastToken = token
	}

	return token, nil
}

// CreateTokenSourceFromCached creates an oauth2.TokenSource from cached tokens.
// The returned token source will automatically refresh the token when it expires,
// using the refresh token.
func CreateTokenSourceFromCached(
	config *oauth2.Config,
	accessToken, refreshToken string,
	expiry time.Time,
) oauth2.TokenSource {
	token := &oauth2.Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Expiry:       expiry,
		TokenType:    "Bearer",
	}

	// ReuseTokenSource will automatically refresh the token when it expires
	return oauth2.ReuseTokenSource(token, config.TokenSource(context.TODO(), token))
}
