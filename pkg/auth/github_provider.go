// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// GitHubTokenCheckURL is the base URL pattern for GitHub OAuth token validation
//
//nolint:gosec // This is a URL pattern, not a credential
const GitHubTokenCheckURL = "api.github.com/applications"

// GitHubProvider implements token introspection for GitHub.com's OAuth token validation API
// GitHub uses a non-standard token validation endpoint that differs from RFC 7662
// Endpoint: POST /applications/{client_id}/token
// Auth: Basic (client_id:client_secret)
// Body: {"access_token": "gho_..."}
//
// Note: This provider is designed for GitHub.com only, not GitHub Enterprise Server
type GitHubProvider struct {
	client       *http.Client
	clientID     string
	clientSecret string
	baseURL      string
	rateLimiter  *rate.Limiter
}

// NewGitHubProvider creates a new GitHub token introspection provider
// Parameters:
//   - introspectURL: GitHub token validation endpoint (must be api.github.com with HTTPS)
//   - clientID: OAuth App client ID
//   - clientSecret: OAuth App client secret
//   - caCertPath: Path to CA certificate bundle (optional)
//   - allowPrivateIP: Allow private IP addresses (should be false for production)
func NewGitHubProvider(
	introspectURL, clientID, clientSecret, caCertPath string,
	allowPrivateIP bool,
) (*GitHubProvider, error) {
	return newGitHubProviderWithClient(introspectURL, clientID, clientSecret, caCertPath, allowPrivateIP, nil)
}

// newGitHubProviderWithClient creates a new GitHub provider with custom client (for testing)
func newGitHubProviderWithClient(
	introspectURL, clientID, clientSecret, caCertPath string,
	allowPrivateIP bool,
	customClient *http.Client,
) (*GitHubProvider, error) {
	var client *http.Client
	var err error

	if customClient != nil {
		// Use provided client (for testing)
		client = customClient
	} else {
		// Create secured HTTP client
		// Note: insecureAllowHTTP is always false for GitHub.com (requires HTTPS)
		client, err = networking.NewHttpClientBuilder().
			WithCABundle(caCertPath).
			WithPrivateIPs(allowPrivateIP).
			Build()
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP client: %w", err)
		}
	}

	// Create rate limiter: 100 requests per second with burst of 200
	// GitHub API allows 5,000 requests/hour, but we rate limit locally to prevent abuse
	limiter := rate.NewLimiter(100, 200)

	return &GitHubProvider{
		client:       client,
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      introspectURL,
		rateLimiter:  limiter,
	}, nil
}

// Name returns the provider name
func (*GitHubProvider) Name() string {
	return "github"
}

// CanHandle returns true if this provider can handle the given introspection URL
// This validates that the URL is a legitimate GitHub.com token validation endpoint
// Note: GitHub Enterprise Server is NOT supported - use corporate IdP instead
func (*GitHubProvider) CanHandle(introspectURL string) bool {
	// Parse URL to validate structure
	u, err := url.Parse(introspectURL)
	if err != nil {
		return false
	}

	// Validate scheme (must be HTTPS)
	if u.Scheme != "https" {
		return false
	}

	// Validate host - must be exactly api.github.com (GitHub.com only, no enterprise)
	if u.Host != "api.github.com" {
		return false
	}

	// Validate path structure: /applications/{client_id}/token
	path := u.Path
	return strings.Contains(path, "/applications/") && strings.HasSuffix(path, "/token")
}

// IntrospectToken introspects a GitHub OAuth token and returns JWT claims
// This calls GitHub's token validation API to verify the token and extract user information
func (g *GitHubProvider) IntrospectToken(ctx context.Context, token string) (jwt.MapClaims, error) {
	logger.Debugf("Using GitHub token validation provider: %s", g.baseURL)

	// Apply rate limiting to prevent DoS and respect GitHub API limits
	if err := g.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	// Create request body with the access token
	reqBody := map[string]string{"access_token": token}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Create POST request
	req, err := http.NewRequestWithContext(ctx, "POST", g.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub validation request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oauth.UserAgent)

	// GitHub requires Basic Auth with OAuth App credentials
	req.SetBasicAuth(g.clientID, g.clientSecret)

	// Make the request
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github validation request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debugf("Failed to close response body: %v", err)
		}
	}()

	// Read the response with a reasonable limit to prevent DoS attacks
	const maxResponseSize = 64 * 1024 // 64KB should be more than enough
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub validation response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode == http.StatusNotFound {
		// 404 means token is invalid or doesn't belong to this OAuth App
		return nil, ErrInvalidToken
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// 429 means we've hit GitHub's rate limit
		retryAfter := resp.Header.Get("Retry-After")
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		reset := resp.Header.Get("X-RateLimit-Reset")
		logger.Warnf("GitHub rate limit exceeded - Retry-After: %s, Remaining: %s, Reset: %s",
			retryAfter, remaining, reset)
		return nil, fmt.Errorf("github rate limit exceeded, retry after: %s", retryAfter)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github validation failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the GitHub response and convert to JWT claims
	logger.Debugf("Successfully validated GitHub token (status: %d)", resp.StatusCode)
	return g.parseGitHubResponse(body)
}

// parseGitHubResponse parses GitHub's token validation response and converts it to JWT claims
func (*GitHubProvider) parseGitHubResponse(body []byte) (jwt.MapClaims, error) {
	// Parse GitHub's response format
	// Reference: https://docs.github.com/en/rest/apps/oauth-applications#check-a-token
	var githubResp struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
		User  struct {
			Login     string `json:"login"`
			ID        int64  `json:"id"`
			NodeID    string `json:"node_id"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Type      string `json:"type"`
			SiteAdmin bool   `json:"site_admin"`
		} `json:"user"`
		Scopes    []string `json:"scopes"`
		CreatedAt string   `json:"created_at"`
		UpdatedAt string   `json:"updated_at"`
		App       struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			ClientID string `json:"client_id"`
		} `json:"app"`
	}

	if err := json.Unmarshal(body, &githubResp); err != nil {
		return nil, fmt.Errorf("failed to decode GitHub response: %w", err)
	}

	// Convert to JWT MapClaims format
	claims := jwt.MapClaims{
		"iss": "https://github.com", // Fixed issuer for GitHub
		"aud": "https://github.com", // Use issuer as audience
		// Mark token as active (consistent with RFC 7662 behavior)
		"active": true,
	}

	// Subject (sub) - use GitHub user ID as the unique identifier
	if githubResp.User.ID != 0 {
		claims["sub"] = fmt.Sprintf("%d", githubResp.User.ID)
	} else {
		return nil, fmt.Errorf("missing user ID in GitHub response")
	}

	// User information
	if githubResp.User.Login != "" {
		claims["preferred_username"] = githubResp.User.Login
		claims["login"] = githubResp.User.Login // GitHub-specific claim
	}
	if githubResp.User.Email != "" {
		claims["email"] = githubResp.User.Email
	}
	if githubResp.User.Name != "" {
		claims["name"] = githubResp.User.Name
	}

	// Parse created_at for iat (issued at) claim
	if githubResp.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, githubResp.CreatedAt); err == nil {
			claims["iat"] = float64(t.Unix())
		}
	}

	// Add scopes - GitHub returns them as an array
	if len(githubResp.Scopes) > 0 {
		claims["scopes"] = githubResp.Scopes
		// Also add as space-separated string for compatibility
		claims["scope"] = strings.Join(githubResp.Scopes, " ")
	}

	// GitHub-specific claims for advanced policies
	if githubResp.User.Type != "" {
		claims["user_type"] = githubResp.User.Type
	}
	if githubResp.User.SiteAdmin {
		claims["site_admin"] = true
	}
	if githubResp.App.Name != "" {
		claims["app_name"] = githubResp.App.Name
	}

	// Note: GitHub OAuth tokens don't have a standard expiration
	// They remain valid until revoked by the user or the app
	// We rely on the introspection call to validate token freshness

	return claims, nil
}
