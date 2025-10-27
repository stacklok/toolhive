// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

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

// UserAgent is the user agent for the ToolHive MCP client
const UserAgent = "ToolHive/1.0"

// OIDCDiscoveryDocument represents the OIDC discovery document structure
// This is a simplified wrapper around the Zitadel OIDC discovery
type OIDCDiscoveryDocument struct {
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

// DiscoverOIDCEndpoints discovers OAuth endpoints from an OIDC issuer
func DiscoverOIDCEndpoints(ctx context.Context, issuer string) (*OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClient(ctx, issuer, nil, false)
}

// DiscoverActualIssuer discovers the actual issuer from a URL that might be different from the issuer itself
// This is useful when the resource metadata points to a URL that hosts the authorization server metadata
// but the actual issuer identifier is different (e.g., Stripe's case)
func DiscoverActualIssuer(ctx context.Context, metadataURL string) (*OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClientAndValidation(ctx, metadataURL, nil, false, false)
}

// discoverOIDCEndpointsWithClient discovers OAuth endpoints from an OIDC issuer with a custom HTTP client (private for testing)
func discoverOIDCEndpointsWithClient(
	ctx context.Context,
	issuer string,
	client httpClient,
	insecureAllowHTTP bool,
) (*OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClientAndValidation(ctx, issuer, client, true, insecureAllowHTTP)
}

// discoverOIDCEndpointsWithClientAndValidation discovers OAuth endpoints with optional issuer validation
//
//nolint:gocyclo // Function complexity justified by comprehensive OIDC discovery logic
func discoverOIDCEndpointsWithClientAndValidation(
	ctx context.Context,
	issuer string,
	client httpClient,
	validateIssuer bool,
	insecureAllowHTTP bool,
) (*OIDCDiscoveryDocument, error) {
	// Validate issuer URL
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Ensure HTTPS for security (except localhost for development or when explicitly allowed for testing)
	if issuerURL.Scheme != "https" && !networking.IsLocalhost(issuerURL.Host) && !insecureAllowHTTP {
		return nil, fmt.Errorf("issuer must use HTTPS: %s (use insecureAllowHTTP for testing only)", issuer)
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

	try := func(urlStr string, oidc bool) (*OIDCDiscoveryDocument, error) {
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
		var doc OIDCDiscoveryDocument
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&doc); err != nil {
			return nil, fmt.Errorf("%s: unexpected response: %w", urlStr, err)
		}
		expectedIssuer := issuer
		if !validateIssuer && doc.Issuer != "" {
			// When not validating, use the discovered issuer as the expected one
			expectedIssuer = doc.Issuer
		}
		if err := validateOIDCDocument(&doc, expectedIssuer, oidc, insecureAllowHTTP); err != nil {
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

// validateOIDCDocument validates the OIDC discovery document
func validateOIDCDocument(doc *OIDCDiscoveryDocument, expectedIssuer string, oidc bool, insecureAllowHTTP bool) error {
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

	// Validate URLs (skip HTTPS validation if insecureAllowHTTP is enabled)
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
			if err := networking.ValidateEndpointURLWithInsecure(endpoint, insecureAllowHTTP); err != nil {
				return fmt.Errorf("invalid %s: %w", name, err)
			}
		}
	}
	return nil
}

// CreateOAuthConfigFromOIDC creates an OAuth config from OIDC discovery
func CreateOAuthConfigFromOIDC(
	ctx context.Context,
	issuer, clientID, clientSecret string,
	scopes []string,
	usePKCE bool,
	callbackPort int,
) (*Config, error) {
	return createOAuthConfigFromOIDCWithClient(ctx, issuer, clientID, clientSecret, scopes, usePKCE, callbackPort, nil)
}

// createOAuthConfigFromOIDCWithClient creates an OAuth config from OIDC discovery with a custom HTTP client (private for testing)
func createOAuthConfigFromOIDCWithClient(
	ctx context.Context,
	issuer, clientID, clientSecret string,
	scopes []string,
	usePKCE bool,
	callbackPort int,
	client httpClient,
) (*Config, error) {
	// Discover OIDC endpoints (insecureAllowHTTP is false for OAuth config creation)
	doc, err := discoverOIDCEndpointsWithClient(ctx, issuer, client, false)
	if err != nil {
		return nil, fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Default scopes for OIDC if none provided
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}

	// Enable PKCE by default if supported
	if !usePKCE && len(doc.CodeChallengeMethodsSupported) > 0 {
		for _, method := range doc.CodeChallengeMethodsSupported {
			if method == "S256" {
				usePKCE = true
				break
			}
		}
	}

	return &Config{
		ClientID:              clientID,
		ClientSecret:          clientSecret,
		AuthURL:               doc.AuthorizationEndpoint,
		IntrospectionEndpoint: doc.IntrospectionEndpoint,
		TokenURL:              doc.TokenEndpoint,
		Scopes:                scopes,
		UsePKCE:               usePKCE,
		CallbackPort:          callbackPort,
	}, nil
}
