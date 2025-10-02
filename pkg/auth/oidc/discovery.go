// Package oidc provides OpenID Connect discovery and configuration functionality.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
)

// UserAgent is the user agent for OIDC/OAuth requests
const UserAgent = "ToolHive/1.0"

// DiscoveryDocument represents the OIDC discovery document structure
type DiscoveryDocument struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	IntrospectionEndpoint         string   `json:"introspection_endpoint,omitempty"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	UserinfoEndpoint              string   `json:"userinfo_endpoint"`
	JWKSURI                       string   `json:"jwks_uri"`
	RegistrationEndpoint          string   `json:"registration_endpoint,omitempty"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

// httpClient interface for dependency injection (private for testing)
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// DiscoverEndpoints discovers OAuth endpoints from an OIDC issuer
func DiscoverEndpoints(ctx context.Context, issuer string) (*DiscoveryDocument, error) {
	return discoverEndpointsWithClient(ctx, issuer, nil, true)
}

// DiscoverActualIssuer discovers the actual issuer from a URL that might be different from the issuer itself
// This is useful when the resource metadata points to a URL that hosts the authorization server metadata
// but the actual issuer identifier is different (e.g., Stripe's case)
func DiscoverActualIssuer(ctx context.Context, metadataURL string) (*DiscoveryDocument, error) {
	return discoverEndpointsWithClient(ctx, metadataURL, nil, false)
}

// DiscoverEndpointsWithOptions discovers OAuth endpoints with custom HTTP client options
func DiscoverEndpointsWithOptions(
	ctx context.Context,
	issuer, caCertPath, authTokenFile string,
	allowPrivateIP bool,
) (*DiscoveryDocument, error) {
	// Create HTTP client with CA bundle and auth token support
	client, err := networking.NewHttpClientBuilder().
		WithCABundle(caCertPath).
		WithTokenFromFile(authTokenFile).
		WithPrivateIPs(allowPrivateIP).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	return discoverEndpointsWithClient(ctx, issuer, client, true)
}

// discoverEndpointsWithClient discovers OAuth endpoints with a custom HTTP client and optional issuer validation
func discoverEndpointsWithClient(
	ctx context.Context,
	issuer string,
	client httpClient,
	validateIssuer bool,
) (*DiscoveryDocument, error) {
	// Validate issuer URL
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Ensure HTTPS for security (except localhost for development)
	if issuerURL.Scheme != "https" && !networking.IsLocalhost(issuerURL.Host) {
		return nil, fmt.Errorf("issuer must use HTTPS: %s", issuer)
	}

	// Build both well-known URLs (handles tenant/realm paths)
	base := issuerURL.Scheme + "://" + issuerURL.Host
	tenant := strings.Trim(issuerURL.EscapedPath(), "/")
	oidcURL := base + path.Join("/", tenant, ".well-known", "openid-configuration")
	oauthURL := base + path.Join("/.well-known/oauth-authorization-server", tenant)

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		}
	}

	try := func(urlStr string, oidc bool) (*DiscoveryDocument, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", urlStr, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", urlStr, resp.StatusCode)
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(ct, "application/json") {
			return nil, fmt.Errorf("%s: unexpected content-type %q", urlStr, ct)
		}

		// Limit response size to prevent DoS
		const maxResponseSize = 1024 * 1024 // 1MB
		var doc DiscoveryDocument
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&doc); err != nil {
			return nil, fmt.Errorf("%s: unexpected response: %w", urlStr, err)
		}
		expectedIssuer := issuer
		if !validateIssuer && doc.Issuer != "" {
			// When not validating, use the discovered issuer as the expected one
			expectedIssuer = doc.Issuer
		}
		if err := validateDocument(&doc, expectedIssuer, oidc); err != nil {
			return nil, fmt.Errorf("%s: invalid metadata: %w", urlStr, err)
		}
		return &doc, nil
	}

	doc, err := try(oidcURL, true)
	if err == nil {
		return doc, nil
	}
	oidcErr := err
	doc, err = try(oauthURL, false)
	if err == nil {
		return doc, nil
	}
	oauthErr := err

	return nil, fmt.Errorf("unable to discover OIDC endpoints at %q or %q: OIDC error: %v, OAuth error: %v",
		oidcURL, oauthURL, oidcErr, oauthErr)
}

// validateDocument validates the OIDC discovery document
func validateDocument(doc *DiscoveryDocument, expectedIssuer string, oidc bool) error {
	if doc.Issuer == "" {
		return fmt.Errorf("missing issuer")
	}

	if doc.Issuer != expectedIssuer {
		return fmt.Errorf("issuer mismatch: expected %s, got %s", expectedIssuer, doc.Issuer)
	}

	if doc.AuthorizationEndpoint == "" {
		return fmt.Errorf("missing authorization_endpoint")
	}

	if doc.TokenEndpoint == "" {
		return fmt.Errorf("missing token_endpoint")
	}

	// Require jwks_uri for OIDC
	if oidc && doc.JWKSURI == "" {
		return fmt.Errorf("missing jwks_uri (OIDC requires it)")
	}

	// Validate URLs
	endpoints := map[string]string{
		"authorization_endpoint": doc.AuthorizationEndpoint,
		"token_endpoint":         doc.TokenEndpoint,
		"jwks_uri":               doc.JWKSURI,
		"introspection_endpoint": doc.IntrospectionEndpoint,
	}

	if doc.UserinfoEndpoint != "" {
		endpoints["userinfo_endpoint"] = doc.UserinfoEndpoint
	}
	for name, endpoint := range endpoints {
		if endpoint != "" {
			if err := networking.ValidateEndpointURL(endpoint); err != nil {
				return fmt.Errorf("invalid %s: %w", name, err)
			}
		}
	}
	return nil
}
