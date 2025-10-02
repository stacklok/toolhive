package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth/oidc"
	"github.com/stacklok/toolhive/pkg/networking"
)

// RFC7662Introspector implements standard RFC 7662 OAuth 2.0 Token Introspection
type RFC7662Introspector struct {
	client       *http.Client
	clientID     string
	clientSecret string
	url          string
}

// NewRFC7662Introspector creates a new RFC 7662 token introspection provider
func NewRFC7662Introspector(introspectURL string) *RFC7662Introspector {
	return &RFC7662Introspector{
		client: http.DefaultClient,
		url:    introspectURL,
	}
}

// NewRFC7662IntrospectorWithAuth creates a new RFC 7662 provider with client credentials
func NewRFC7662IntrospectorWithAuth(
	introspectURL, clientID, clientSecret, caCertPath, authTokenFile string, allowPrivateIP bool,
) (*RFC7662Introspector, error) {
	// Create HTTP client with CA bundle and auth token support
	client, err := networking.NewHttpClientBuilder().
		WithCABundle(caCertPath).
		WithTokenFromFile(authTokenFile).
		WithPrivateIPs(allowPrivateIP).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	return &RFC7662Introspector{
		client:       client,
		clientID:     clientID,
		clientSecret: clientSecret,
		url:          introspectURL,
	}, nil
}

// Name returns the provider name
func (*RFC7662Introspector) Name() string {
	return "rfc7662"
}

// CanHandle returns true if this provider can handle the given introspection URL
func (r *RFC7662Introspector) CanHandle(introspectURL string) bool {
	// If no URL was configured, this is a fallback provider that handles everything
	if r.url == "" {
		return true
	}
	// Otherwise, only handle the specific configured URL
	return r.url == introspectURL
}

// IntrospectToken introspects a token using RFC 7662 standard
func (r *RFC7662Introspector) IntrospectToken(ctx context.Context, tokenStr string) (jwt.MapClaims, error) {
	// Prepare form data for POST request
	formData := url.Values{}
	formData.Set("token", tokenStr)
	formData.Set("token_type_hint", "access_token")

	// Create POST request with form data
	req, err := http.NewRequestWithContext(ctx, "POST", r.url, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create introspection request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", oidc.UserAgent)
	req.Header.Set("Accept", "application/json")

	// Add client authentication if configured
	if r.clientID != "" && r.clientSecret != "" {
		req.SetBasicAuth(r.clientID, r.clientSecret)
	}

	// Make the request
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with a reasonable limit to prevent DoS attacks
	const maxResponseSize = 64 * 1024 // 64KB
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read introspection response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("introspection unauthorized")
		}
		return nil, fmt.Errorf("introspection failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse RFC 7662 response
	return parseIntrospectionClaims(strings.NewReader(string(body)))
}
