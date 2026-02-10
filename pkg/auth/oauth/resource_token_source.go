// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
)

// resourceTokenSource wraps an oauth2.TokenSource and adds the resource parameter
// to token refresh requests per RFC 8707. This is necessary because the standard
// golang.org/x/oauth2 library doesn't support adding custom parameters during refresh.
type resourceTokenSource struct {
	config     *oauth2.Config
	resource   string
	token      *oauth2.Token
	httpClient *http.Client
}

// newResourceTokenSource creates a token source that includes the resource parameter
// in all token requests, including refresh requests.
// The resource parameter must be non-empty (caller should check before calling).
func newResourceTokenSource(config *oauth2.Config, token *oauth2.Token, resource string) oauth2.TokenSource {
	return &resourceTokenSource{
		config:   config,
		resource: resource,
		token:    token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Token returns a valid token, refreshing it if necessary.
// When refreshing, it adds the resource parameter to the request.
func (r *resourceTokenSource) Token() (*oauth2.Token, error) {
	// If token is still valid, return it
	if r.token.Valid() {
		return r.token, nil
	}

	// Token expired, refresh with resource parameter
	ctx := context.Background()
	newToken, err := r.refreshWithResource(ctx)
	if err != nil {
		return nil, err
	}

	r.token = newToken
	return newToken, nil
}

// refreshWithResource performs token refresh with the resource parameter per RFC 8707.
func (r *resourceTokenSource) refreshWithResource(ctx context.Context) (*oauth2.Token, error) {
	if r.token == nil || r.token.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	// Use x/oauth2 internals via Exchange() so we get:
	// - expires_in -> Expiry handling
	// - defaulting to HTTP Basic auth (or auto-detection)
	// - structured error parsing via oauth2.RetrieveError
	//
	// Note: Exchange() will include an empty "code" parameter in the body.
	// This is a known workaround for adding custom refresh params.
	refreshToken := r.token.RefreshToken
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("grant_type", "refresh_token"),
		oauth2.SetAuthURLParam("refresh_token", refreshToken),
		oauth2.SetAuthURLParam("resource", r.resource),
	}

	ctx = context.WithValue(ctx, oauth2.HTTPClient, r.httpClient)

	token, err := r.config.Exchange(ctx, "", opts...)
	if err != nil {
		return nil, fmt.Errorf("token refresh with resource parameter failed: %w", err)
	}

	// Preserve refresh token if the server does not return a new one.
	if token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}

	logger.Debugw("token refreshed with resource parameter",
		"resource", r.resource,
	)

	return token, nil
}
