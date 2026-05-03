// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// discoveryTimeout is the bounded per-call timeout applied when fetching
// authorization server metadata, regardless of the caller's context deadline.
//
// Declared as a var rather than a const so tests that need to exercise the
// timeout path can shorten it without waiting 10 s per run. Production code
// MUST NOT mutate this value — it is effectively constant outside tests.
var discoveryTimeout = 10 * time.Second

// discoveryMaxResponseSize caps the response body size accepted from a
// discovery endpoint to prevent resource exhaustion from hostile servers.
const discoveryMaxResponseSize = 1024 * 1024 // 1MB

// FetchAuthorizationServerMetadata fetches RFC 8414 authorization server
// metadata for the given issuer, using a three-path fallback that mirrors
// the guidance in RFC 8414 §3.1 and OpenID Connect Discovery 1.0.
//
// The paths are tried in this order:
//  1. RFC 8414 path-insertion: {origin}/.well-known/oauth-authorization-server{path}
//  2. OIDC Discovery:          {issuer}/.well-known/openid-configuration
//  3. Bare RFC 8414:           {origin}/.well-known/oauth-authorization-server
//
// A 200 OK response with a clean application/json body that passes the
// RFC 8414 §3.3 issuer check is a candidate. The first candidate with a
// non-empty registration_endpoint wins immediately. If no candidate has a
// registration_endpoint but at least one was otherwise valid, the first such
// partial document is returned with ErrRegistrationEndpointMissing — never
// short-circuiting on a partial doc, since a tenant-aware IdP may publish
// path-insertion metadata without DCR while the OIDC document advertises it.
//
// If client is nil, a default *http.Client with a bounded 10 s timeout is
// used. When client is non-nil, the same bounded 10 s per-call timeout is
// applied via context.WithTimeout on top of the caller's context to protect
// against callers that pass context.Background or a long deadline.
//
// Return contract:
//
//   - On full success, returns (metadata, nil) with a non-empty
//     RegistrationEndpoint.
//
//   - When at least one candidate document was issuer-validated but every such
//     document omits registration_endpoint, returns the first partial document
//     paired with ErrRegistrationEndpointMissing. The metadata is non-nil so
//     callers can reuse the other fields (TokenEndpoint, JWKSURI, etc.). Note
//     this deviates from the usual Go "err != nil ⇒ value is invalid" idiom;
//     callers must check with errors.Is and explicitly decide whether to use
//     the partial doc:
//
//     md, err := FetchAuthorizationServerMetadata(ctx, issuer, nil)
//     switch {
//     case errors.Is(err, ErrRegistrationEndpointMissing):
//     // md is non-nil; DCR unavailable but token/JWKS endpoints usable.
//     case err != nil:
//     // md is nil; discovery failed.
//     default:
//     // md fully populated.
//     }
//
//   - On any other failure (no URL served a valid doc, transport/decode error,
//     issuer mismatch), returns (nil, err). The returned error wraps the
//     per-attempt errors via errors.Join so callers can use errors.Is /
//     errors.As to inspect underlying causes (e.g. context.DeadlineExceeded).
func FetchAuthorizationServerMetadata(
	ctx context.Context,
	issuer string,
	client *http.Client,
) (*AuthorizationServerMetadata, error) {
	urls, err := buildDiscoveryURLs(issuer)
	if err != nil {
		return nil, err
	}

	httpClient := buildDiscoveryHTTPClient(client)

	// Bound per-call timeout regardless of caller context.
	fetchCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	var attemptErrs []error
	var partialMetadata *AuthorizationServerMetadata
	for _, u := range urls {
		metadata, err := fetchDiscoveryDocument(fetchCtx, httpClient, u, issuer)
		if err != nil {
			attemptErrs = append(attemptErrs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		if metadata.RegistrationEndpoint == "" {
			// Stash the first partial doc as a fallback, but keep iterating:
			// a later URL (e.g. OIDC discovery) may advertise DCR even when
			// the first hit (e.g. tenant-aware path-insertion) does not.
			if partialMetadata == nil {
				partialMetadata = metadata
			}
			continue
		}
		return metadata, nil
	}

	if partialMetadata != nil {
		return partialMetadata, ErrRegistrationEndpointMissing
	}

	return nil, fmt.Errorf(
		"failed to discover authorization server metadata for issuer %q: %w",
		issuer, errors.Join(attemptErrs...),
	)
}

// buildDiscoveryURLs constructs the discovery URLs to try per RFC 8414 §3.1
// and OpenID Connect Discovery 1.0. The issuer must have an "https" scheme
// (or "http" with a loopback host, for development).
//
// URLs are returned in priority order with exact duplicates removed: for an
// issuer with no tenant path, path-insertion (1) and bare RFC 8414 (3)
// collapse to the same URL, so only two distinct URLs are returned. Callers
// must not rely on a fixed slice length.
func buildDiscoveryURLs(issuer string) ([]string, error) {
	if issuer == "" {
		return nil, fmt.Errorf("issuer is required")
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("invalid issuer URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid issuer URL: scheme and host are required")
	}
	if parsed.Scheme != schemeHTTPS && !IsLoopbackHost(parsed.Host) {
		return nil, fmt.Errorf("issuer must use https (got %q)", parsed.Scheme)
	}

	// Strip trailing slash from issuer path so URL joins stay predictable.
	// Use the unescaped Path: assigning back into url.URL.Path means String()
	// will escape exactly once. Using EscapedPath() here would re-escape
	// percent-encoded tenants and produce e.g. "%252F" from "%2F".
	tenant := strings.Trim(parsed.Path, "/")

	origin := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}

	// (1) RFC 8414 §3.1 path-insertion: /.well-known/oauth-authorization-server/{tenant}
	pathInsertion := *origin
	pathInsertion.Path = path.Join(WellKnownOAuthServerPath, tenant)

	// (2) OIDC Discovery: {issuer}/.well-known/openid-configuration
	oidc := *origin
	oidc.Path = path.Join("/", tenant, WellKnownOIDCPath)

	// (3) Bare RFC 8414: {origin}/.well-known/oauth-authorization-server
	bare := *origin
	bare.Path = WellKnownOAuthServerPath

	// Deduplicate while preserving order. A tenant-less issuer causes (1) and
	// (3) to collapse to the same URL; emitting both would waste a round-trip
	// and produce confusing duplicate entries in the aggregated error message.
	candidates := []string{pathInsertion.String(), oidc.String(), bare.String()}
	seen := make(map[string]struct{}, len(candidates))
	urls := make([]string, 0, len(candidates))
	for _, u := range candidates {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls, nil
}

// buildDiscoveryHTTPClient returns the caller-supplied client, or a default
// bounded client. The per-call timeout enforced via context.WithTimeout in
// FetchAuthorizationServerMetadata guarantees the bound is honored even when
// the caller supplies a client with a larger or missing Timeout.
func buildDiscoveryHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{
		Timeout: discoveryTimeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}
}

// FetchAuthorizationServerMetadataFromURL fetches RFC 8414 authorization
// server metadata from a single caller-supplied URL, bypassing the
// well-known-path fallback used by FetchAuthorizationServerMetadata.
//
// It is intended for cases where the operator has configured an explicit
// discovery document URL (for example a multi-tenant IdP that does not
// advertise the tenant-aware path at {issuer}/.well-known/...). The same
// RFC 8414 §3.3 issuer-equality check is enforced: the returned metadata's
// issuer field must exactly match the caller-supplied expectedIssuer.
//
// If client is nil, the same bounded default client used by
// FetchAuthorizationServerMetadata is constructed. A 10 s per-call timeout
// is applied via context.WithTimeout regardless of the caller's context
// deadline.
//
// Return contract mirrors FetchAuthorizationServerMetadata:
//
//   - On full success, returns (metadata, nil) with a non-empty
//     RegistrationEndpoint.
//   - When the document is otherwise valid but omits
//     registration_endpoint, returns (metadata, ErrRegistrationEndpointMissing).
//     The metadata is non-nil so callers can reuse the other fields.
//   - On any other failure (transport/decode error, issuer mismatch),
//     returns (nil, err).
func FetchAuthorizationServerMetadataFromURL(
	ctx context.Context,
	discoveryURL string,
	expectedIssuer string,
	client *http.Client,
) (*AuthorizationServerMetadata, error) {
	if discoveryURL == "" {
		return nil, fmt.Errorf("discovery URL is required")
	}
	if expectedIssuer == "" {
		return nil, fmt.Errorf("expected issuer is required")
	}

	parsed, err := url.Parse(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("invalid discovery URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid discovery URL: scheme and host are required")
	}
	if parsed.Scheme != schemeHTTPS && !IsLoopbackHost(parsed.Host) {
		return nil, fmt.Errorf("discovery URL must use https (got %q)", parsed.Scheme)
	}

	httpClient := buildDiscoveryHTTPClient(client)

	fetchCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	metadata, err := fetchDiscoveryDocument(fetchCtx, httpClient, discoveryURL, expectedIssuer)
	if err != nil {
		return nil, fmt.Errorf("fetch discovery document from %q: %w", discoveryURL, err)
	}
	if metadata.RegistrationEndpoint == "" {
		return metadata, ErrRegistrationEndpointMissing
	}
	return metadata, nil
}

// fetchDiscoveryDocument performs a single GET against a discovery URL and
// returns the parsed AuthorizationServerMetadata, enforcing RFC 8414 §3.3
// issuer equality.
func fetchDiscoveryDocument(
	ctx context.Context,
	client *http.Client,
	urlStr string,
	expectedIssuer string,
) (*AuthorizationServerMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := client.Do(req) //nolint:gosec // G107: URL is constructed from configured issuer
	if err != nil {
		return nil, fmt.Errorf("GET failed: %w", err)
	}
	defer func() {
		// Drain fully before closing so the underlying TCP connection is
		// eligible for reuse. The JSON decode path below caps the accepted
		// body via io.LimitReader; we intentionally do NOT limit the drain
		// here because a bounded drain leaves bytes in the kernel buffer
		// and defeats connection reuse.
		_, _ = io.Copy(io.Discard, resp.Body)
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Debug("failed to close discovery response body", "error", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("unexpected content-type %q", contentType)
	}

	var metadata AuthorizationServerMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, discoveryMaxResponseSize)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// RFC 8414 §3.3: the issuer in the metadata MUST exactly match the caller's expected issuer.
	if metadata.Issuer != expectedIssuer {
		return nil, fmt.Errorf("issuer mismatch (RFC 8414 §3.3): expected %q, got %q", expectedIssuer, metadata.Issuer)
	}

	return &metadata, nil
}

// AuthorizationServerMetadata represents the OAuth 2.0 Authorization Server Metadata
// per RFC 8414. This is the base structure that OIDC Discovery extends.
type AuthorizationServerMetadata struct {
	// Issuer is the authorization server's issuer identifier (REQUIRED per RFC 8414).
	Issuer string `json:"issuer"`

	// AuthorizationEndpoint is the URL of the authorization endpoint (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	AuthorizationEndpoint string `json:"authorization_endpoint"`

	// TokenEndpoint is the URL of the token endpoint (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	TokenEndpoint string `json:"token_endpoint"`

	// JWKSURI is the URL of the JSON Web Key Set document (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	JWKSURI string `json:"jwks_uri"`

	// RegistrationEndpoint is the URL of the Dynamic Client Registration endpoint (OPTIONAL).
	RegistrationEndpoint string `json:"registration_endpoint,omitempty"`

	// IntrospectionEndpoint is the URL of the token introspection endpoint (OPTIONAL, RFC 7662).
	IntrospectionEndpoint string `json:"introspection_endpoint,omitempty"`

	// UserinfoEndpoint is the URL of the UserInfo endpoint (RECOMMENDED per OIDC Discovery, not in RFC 8414).
	// Omitted from JSON when empty to avoid serializing an invalid URL value.
	UserinfoEndpoint string `json:"userinfo_endpoint,omitempty"`

	// ResponseTypesSupported lists the response types supported (RECOMMENDED).
	ResponseTypesSupported []string `json:"response_types_supported,omitempty"`

	// GrantTypesSupported lists the grant types supported (OPTIONAL).
	GrantTypesSupported []string `json:"grant_types_supported,omitempty"`

	// CodeChallengeMethodsSupported lists the PKCE code challenge methods supported (OPTIONAL).
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`

	// TokenEndpointAuthMethodsSupported lists the authentication methods supported at the token endpoint (OPTIONAL).
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`

	// ScopesSupported lists the OAuth 2.0 scope values supported (RECOMMENDED per RFC 8414).
	// For MCP authorization servers, this typically includes "openid" and "offline_access".
	ScopesSupported []string `json:"scopes_supported,omitempty"`

	// ClientIDMetadataDocumentSupported indicates the server accepts HTTPS URLs as client_id
	// values per draft-ietf-oauth-client-id-metadata-document.
	ClientIDMetadataDocumentSupported bool `json:"client_id_metadata_document_supported,omitempty"`
}

// OIDCDiscoveryDocument represents the OpenID Connect Discovery 1.0 document.
// It extends OAuth 2.0 Authorization Server Metadata (RFC 8414) with OIDC-specific fields.
// This unified type supports both producer (server) and consumer (client) use cases.
type OIDCDiscoveryDocument struct {
	// Embed OAuth 2.0 AS Metadata (RFC 8414) as the base
	AuthorizationServerMetadata

	// SubjectTypesSupported lists the subject identifier types supported (REQUIRED for OIDC).
	SubjectTypesSupported []string `json:"subject_types_supported,omitempty"`

	// IDTokenSigningAlgValuesSupported lists the JWS algorithms supported for ID tokens (REQUIRED for OIDC).
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported,omitempty"`

	// ClaimsSupported lists the claims that can be returned (RECOMMENDED for OIDC).
	ClaimsSupported []string `json:"claims_supported,omitempty"`
}

// Validate performs basic validation on the discovery document.
// It checks for required fields based on whether this is an OIDC or pure OAuth document.
func (d *OIDCDiscoveryDocument) Validate(isOIDC bool) error {
	if d.Issuer == "" {
		return ErrMissingIssuer
	}
	if d.AuthorizationEndpoint == "" {
		return ErrMissingAuthorizationEndpoint
	}
	if d.TokenEndpoint == "" {
		return ErrMissingTokenEndpoint
	}
	if isOIDC && d.JWKSURI == "" {
		return ErrMissingJWKSURI
	}
	if isOIDC && len(d.ResponseTypesSupported) == 0 {
		return ErrMissingResponseTypesSupported
	}
	return nil
}

// SupportsPKCE returns true if the authorization server supports PKCE with S256.
func (d *OIDCDiscoveryDocument) SupportsPKCE() bool {
	for _, method := range d.CodeChallengeMethodsSupported {
		if method == PKCEMethodS256 {
			return true
		}
	}
	return false
}

// SupportsGrantType returns true if the authorization server supports the given grant type.
func (d *OIDCDiscoveryDocument) SupportsGrantType(grantType string) bool {
	for _, gt := range d.GrantTypesSupported {
		if gt == grantType {
			return true
		}
	}
	return false
}
