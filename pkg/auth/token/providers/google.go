// Package providers contains token introspection provider implementations.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth/oidc"
	"github.com/stacklok/toolhive/pkg/logger"
)

var (
	// ErrInvalidToken is returned when a token is invalid or inactive
	ErrInvalidToken = errors.New("invalid token")
)

// GoogleTokeninfoURL is the Google OAuth2 tokeninfo endpoint URL
const GoogleTokeninfoURL = "https://oauth2.googleapis.com/tokeninfo" //nolint:gosec

// GoogleIntrospector implements token introspection for Google's tokeninfo API
type GoogleIntrospector struct {
	client *http.Client
	url    string
}

// NewGoogleIntrospector creates a new Google token introspection provider
func NewGoogleIntrospector(introspectURL string) *GoogleIntrospector {
	return &GoogleIntrospector{
		client: http.DefaultClient,
		url:    introspectURL,
	}
}

// Name returns the provider name
func (*GoogleIntrospector) Name() string {
	return "google"
}

// CanHandle returns true if this provider can handle the given introspection URL
func (g *GoogleIntrospector) CanHandle(introspectURL string) bool {
	return introspectURL == g.url
}

// IntrospectToken introspects a Google opaque token and returns JWT claims
func (g *GoogleIntrospector) IntrospectToken(ctx context.Context, tokenStr string) (jwt.MapClaims, error) {
	logger.Debugf("Using Google tokeninfo provider for token introspection: %s", g.url)

	// Parse the URL and add query parameters
	u, err := url.Parse(g.url)
	if err != nil {
		return nil, fmt.Errorf("failed to parse introspection URL: %w", err)
	}

	// Add the access token as a query parameter
	query := u.Query()
	query.Set("access_token", tokenStr)
	u.RawQuery = query.Encode()

	// Create the GET request
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Google tokeninfo request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oidc.UserAgent)

	// Make the request
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google tokeninfo request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read the response with a reasonable limit to prevent DoS attacks
	const maxResponseSize = 64 * 1024 // 64KB
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read Google tokeninfo response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google tokeninfo request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the Google response and convert to JWT claims
	logger.Debugf("Successfully received Google tokeninfo response (status: %d)", resp.StatusCode)
	return parseGoogleResponse(body)
}

// parseGoogleResponse parses Google's tokeninfo response and converts it to JWT claims
func parseGoogleResponse(body []byte) (jwt.MapClaims, error) {
	// Parse Google's response format
	var googleResp struct {
		// Standard OAuth fields
		Aud   string `json:"aud,omitempty"`
		Sub   string `json:"sub,omitempty"`
		Scope string `json:"scope,omitempty"`

		// Google returns Unix timestamp as string (RFC 7662 uses numeric)
		Exp string `json:"exp,omitempty"`

		// Google-specific fields
		Azp           string `json:"azp,omitempty"`
		ExpiresIn     string `json:"expires_in,omitempty"`
		Email         string `json:"email,omitempty"`
		EmailVerified string `json:"email_verified,omitempty"`
	}

	if err := json.Unmarshal(body, &googleResp); err != nil {
		return nil, fmt.Errorf("failed to decode Google tokeninfo JSON: %w", err)
	}

	// Convert to JWT MapClaims format
	claims := jwt.MapClaims{
		"iss": "https://accounts.google.com", // Default Google issuer
	}

	// Copy standard fields
	if googleResp.Sub != "" {
		claims["sub"] = googleResp.Sub
	}
	if googleResp.Aud != "" {
		claims["aud"] = googleResp.Aud
	}
	if googleResp.Scope != "" {
		claims["scope"] = googleResp.Scope
	}

	// Handle expiration - convert string timestamp to float64
	if googleResp.Exp != "" {
		expInt, err := strconv.ParseInt(googleResp.Exp, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid exp format: %w", err)
		}
		claims["exp"] = float64(expInt) // JWT expects float64

		// Check if token is expired and return error if so (consistent with RFC 7662 behavior)
		isActive := time.Now().Unix() < expInt
		claims["active"] = isActive
		if !isActive {
			return nil, ErrInvalidToken
		}
	} else {
		return nil, fmt.Errorf("missing exp field in Google response")
	}

	// Copy Google-specific fields
	if googleResp.Azp != "" {
		claims["azp"] = googleResp.Azp
	}
	if googleResp.ExpiresIn != "" {
		claims["expires_in"] = googleResp.ExpiresIn
	}
	if googleResp.Email != "" {
		claims["email"] = googleResp.Email
	}
	if googleResp.EmailVerified != "" {
		claims["email_verified"] = googleResp.EmailVerified
	}

	return claims, nil
}
