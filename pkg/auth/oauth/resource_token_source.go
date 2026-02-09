// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

	// Build refresh request with resource parameter
	v := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {r.token.RefreshToken},
	}

	// Add resource parameter per RFC 8707
	v.Set("resource", r.resource)

	// Add client credentials
	if r.config.ClientID != "" {
		v.Set("client_id", r.config.ClientID)
	}
	if r.config.ClientSecret != "" {
		v.Set("client_secret", r.config.ClientSecret)
	}

	// Make the request
	req, err := http.NewRequestWithContext(ctx, "POST", r.config.Endpoint.TokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status %d", resp.StatusCode)
	}

	// Parse the token response
	var token oauth2.Token
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	logger.Debugw("token refreshed with resource parameter",
		"resource", r.resource,
	)

	return &token, nil
}
