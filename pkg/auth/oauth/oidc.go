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

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

// UserAgent is the user agent for the ToolHive MCP client
const UserAgent = "ToolHive/1.0"

// DiscoverOIDCEndpoints discovers OAuth endpoints from an OIDC issuer
func DiscoverOIDCEndpoints(ctx context.Context, issuer string) (*oauthproto.OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClient(ctx, issuer, nil, false)
}

// DiscoverActualIssuer discovers the actual issuer from a URL that might be different from the issuer itself
// This is useful when the resource metadata points to a URL that hosts the authorization server metadata
// but the actual issuer identifier is different (e.g., Stripe's case)
func DiscoverActualIssuer(ctx context.Context, metadataURL string) (*oauthproto.OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClientAndValidation(ctx, metadataURL, nil, false, false)
}

// discoverOIDCEndpointsWithClient discovers OAuth endpoints from an OIDC issuer with a custom HTTP client (private for testing)
func discoverOIDCEndpointsWithClient(
	ctx context.Context,
	issuer string,
	client networking.HTTPClient,
	insecureAllowHTTP bool,
) (*oauthproto.OIDCDiscoveryDocument, error) {
	return discoverOIDCEndpointsWithClientAndValidation(ctx, issuer, client, true, insecureAllowHTTP)
}

// discoverOIDCEndpointsWithClientAndValidation discovers OAuth endpoints with optional issuer validation
//
//nolint:gocyclo // Function complexity justified by comprehensive OIDC discovery logic
func discoverOIDCEndpointsWithClientAndValidation(
	ctx context.Context,
	issuer string,
	client networking.HTTPClient,
	validateIssuer bool,
	insecureAllowHTTP bool,
) (*oauthproto.OIDCDiscoveryDocument, error) {

	oidcURL, oauthURL, err := buildWellKnownURLs(issuer, insecureAllowHTTP)
	if err != nil {
		return nil, fmt.Errorf("failed to build well-known URLs: %w", err)
	}

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		}
	}

	try := func(urlStr string, oidc bool) (*oauthproto.OIDCDiscoveryDocument, error) {
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
		defer func() {
			if err := resp.Body.Close(); err != nil {
				logger.Debugf("Failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", urlStr, resp.StatusCode)
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if !strings.Contains(ct, "application/json") {
			return nil, fmt.Errorf("%s: unexpected content-type %q", urlStr, ct)
		}

		// Limit response size to prevent DoS
		const maxResponseSize = 1024 * 1024 // 1MB
		var doc oauthproto.OIDCDiscoveryDocument
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
		// OIDC discovery succeeded, but check if we need registration_endpoint
		// If it's missing, try OAuth authorization server well-known URL as fallback
		if doc.RegistrationEndpoint == "" {
			logger.Debugf("OIDC discovery succeeded but registration_endpoint is missing, " +
				"trying OAuth authorization server well-known URL")
			oauthDoc, oauthErr := try(oauthURL, false)
			if oauthErr == nil && oauthDoc.RegistrationEndpoint != "" {
				// Validate issuer matches before merging
				if oauthDoc.Issuer == doc.Issuer {
					doc.RegistrationEndpoint = oauthDoc.RegistrationEndpoint
					logger.Infof("Found registration_endpoint in OAuth authorization server metadata: %s", doc.RegistrationEndpoint)
				} else {
					logger.Warnf("Issuer mismatch between OIDC (%s) and OAuth (%s) discovery documents, not merging registration_endpoint",
						doc.Issuer, oauthDoc.Issuer)
				}
			}
		}
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

// validateOIDCDocument validates the OIDC discovery document.
// It delegates basic field presence validation to the shared doc.Validate() method,
// then performs additional security checks (issuer mismatch, URL HTTPS validation).
func validateOIDCDocument(
	doc *oauthproto.OIDCDiscoveryDocument,
	expectedIssuer string,
	oidc bool,
	insecureAllowHTTP bool,
) error {
	// Delegate basic field presence validation to the shared method.
	// Note: We pass oidc=false here because we handle jwks_uri separately below
	// with a more specific error message, and response_types_supported validation
	// is not enforced in this legacy code path to maintain backward compatibility.
	if err := doc.Validate(false); err != nil {
		return err
	}

	// Require jwks_uri for OIDC (with specific error message)
	if oidc && doc.JWKSURI == "" {
		return fmt.Errorf("missing jwks_uri (OIDC requires it)")
	}

	// Security check: issuer must match expected value
	if doc.Issuer != expectedIssuer {
		return fmt.Errorf("issuer mismatch: expected %s, got %s", expectedIssuer, doc.Issuer)
	}

	// Security check: validate endpoint URLs (HTTPS required unless insecureAllowHTTP)
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
	resource string,
) (*Config, error) {
	return createOAuthConfigFromOIDCWithClient(ctx, issuer, clientID, clientSecret, scopes, usePKCE, callbackPort, resource, nil)
}

// createOAuthConfigFromOIDCWithClient creates an OAuth config from OIDC discovery with a custom HTTP client (private for testing)
func createOAuthConfigFromOIDCWithClient(
	ctx context.Context,
	issuer, clientID, clientSecret string,
	scopes []string,
	usePKCE bool,
	callbackPort int,
	resource string,
	client networking.HTTPClient,
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
			if method == oauthproto.PKCEMethodS256 {
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
		Resource:              resource,
	}, nil
}

func buildWellKnownURLs(issuer string, insecureAllowHTTP bool) (oidcURL, oauthURL string, err error) {
	// Parse issuer
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return "", "", fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Enforce HTTPS except localhost or explicit override
	if issuerURL.Scheme != "https" &&
		!networking.IsLocalhost(issuerURL.Host) &&
		!insecureAllowHTTP {
		return "", "", fmt.Errorf("issuer must use HTTPS: %s (use insecureAllowHTTP for testing only)", issuer)
	}

	// Extract tenant/realm path (may be nested)
	tenant := strings.Trim(issuerURL.EscapedPath(), "/")

	// Base = scheme + host
	base := &url.URL{
		Scheme: issuerURL.Scheme,
		Host:   issuerURL.Host,
	}

	//
	// Build OIDC Discovery URL
	//   /{tenant}/.well-known/openid-configuration
	//
	oidc := *base
	oidc.Path = path.Join("/", tenant, oauthproto.WellKnownOIDCPath)
	oidcURL = oidc.String()

	//
	// Build OAuth AS Metadata URL
	//   /.well-known/oauth-authorization-server/{tenant}
	//
	oauth := *base
	oauth.Path = path.Join(oauthproto.WellKnownOAuthServerPath, tenant)
	oauthURL = oauth.String()

	return oidcURL, oauthURL, nil
}
