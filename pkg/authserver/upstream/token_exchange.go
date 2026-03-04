// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// customExchangeCodeForTokens performs the token exchange HTTP call directly,
// bypassing golang.org/x/oauth2, and extracts fields using the configured mapping.
// This supports providers like GovSlack that nest token fields under non-standard paths.
func (p *BaseOAuth2Provider) customExchangeCodeForTokens(
	ctx context.Context, code, codeVerifier string,
) (*Tokens, error) {
	// Build form data per RFC 6749 Section 4.1.3
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {p.config.RedirectURI},
		"client_id":    {p.config.ClientID},
	}
	if p.config.ClientSecret != "" {
		data.Set("client_secret", p.config.ClientSecret)
	}
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

	tokens, err := p.doCustomTokenRequest(ctx, data)
	if err != nil {
		return nil, err
	}

	slog.Info("authorization code exchange successful (custom mapping)",
		"has_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// customRefreshTokens performs a refresh token request directly,
// bypassing golang.org/x/oauth2, and extracts fields using the configured mapping.
func (p *BaseOAuth2Provider) customRefreshTokens(
	ctx context.Context, refreshToken string,
) (*Tokens, error) {
	// Build form data per RFC 6749 Section 6
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {p.config.ClientID},
	}
	if p.config.ClientSecret != "" {
		data.Set("client_secret", p.config.ClientSecret)
	}

	tokens, err := p.doCustomTokenRequest(ctx, data)
	if err != nil {
		return nil, err
	}

	slog.Info("token refresh successful (custom mapping)",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// doCustomTokenRequest makes the HTTP POST to the token endpoint and extracts
// fields from the response using the configured gjson paths.
func (p *BaseOAuth2Provider) doCustomTokenRequest(
	ctx context.Context, data url.Values,
) (*Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.config.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req) //nolint:gosec // G704: URL is from provider config
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		//nolint:gosec // G706: status code is an integer
		slog.Debug("custom token request failed", "status", resp.StatusCode)
		return nil, fmt.Errorf("token request failed with status %d", resp.StatusCode)
	}

	return extractTokensFromResponse(body, p.config.TokenResponseMapping)
}

// extractTokensFromResponse extracts token fields from raw JSON using gjson paths.
func extractTokensFromResponse(body []byte, mapping *TokenResponseMapping) (*Tokens, error) {
	if mapping == nil {
		return nil, errors.New("token response mapping is required")
	}

	// Required: access_token
	accessToken := gjson.GetBytes(body, mapping.AccessTokenPath).String()
	if accessToken == "" {
		return nil, fmt.Errorf("token response missing access_token at path: %s", mapping.AccessTokenPath)
	}

	// Token type (default path: "token_type")
	tokenTypePath := pathOrDefault(mapping.TokenTypePath, "token_type")
	tokenType := gjson.GetBytes(body, tokenTypePath).String()
	if tokenType == "" {
		tokenType = "Bearer" // Default per RFC 6749
	}
	if !strings.EqualFold(tokenType, "bearer") && !strings.EqualFold(tokenType, "user") {
		return nil, fmt.Errorf("unexpected token_type: expected \"Bearer\", got %q", tokenType)
	}

	// Refresh token (default path: "refresh_token")
	refreshTokenPath := pathOrDefault(mapping.RefreshTokenPath, "refresh_token")
	refreshToken := gjson.GetBytes(body, refreshTokenPath).String()

	// Expires in (default path: "expires_in")
	expiresInPath := pathOrDefault(mapping.ExpiresInPath, "expires_in")
	expiresAt := time.Now().Add(defaultTokenExpiration)
	expiresInResult := gjson.GetBytes(body, expiresInPath)
	if expiresInResult.Exists() && expiresInResult.Int() > 0 {
		expiresAt = time.Now().Add(time.Duration(expiresInResult.Int()) * time.Second)
	}

	// Extract ID token from standard location (not mapped — OIDC only)
	idToken := gjson.GetBytes(body, "id_token").String()

	return &Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// pathOrDefault returns the path if non-empty, otherwise returns the default.
func pathOrDefault(path, defaultPath string) string {
	if path != "" {
		return path
	}
	return defaultPath
}
