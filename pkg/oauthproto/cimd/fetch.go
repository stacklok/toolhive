// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cimd implements fetching and validation of OAuth 2.0 Client ID
// Metadata Documents (CIMD) per draft-ietf-oauth-client-id-metadata-document.
//
// This package is a sub-package of pkg/oauthproto and is allowed to import
// pkg/networking (for SSRF utilities) without violating the leaf-package
// invariant of the parent package.
package cimd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ClientMetadataDocument represents an OAuth 2.0 Client ID Metadata Document
// per draft-ietf-oauth-client-id-metadata-document.
type ClientMetadataDocument struct {
	// Required
	ClientID     string   `json:"client_id"`
	RedirectURIs []string `json:"redirect_uris"`

	// Recommended
	ClientName string `json:"client_name,omitempty"`
	LogoURI    string `json:"logo_uri,omitempty"`
	ClientURI  string `json:"client_uri,omitempty"`

	// Optional
	TosURI                  string   `json:"tos_uri,omitempty"`
	PolicyURI               string   `json:"policy_uri,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	ApplicationType         string   `json:"application_type,omitempty"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`
}

// forbiddenAuthMethods lists token_endpoint_auth_method values that MUST NOT
// appear in a CIMD document. Per the spec, CIMD clients may not use symmetric
// shared-secret methods because no pre-shared secret is ever established.
var forbiddenAuthMethods = []string{
	"client_secret_post",
	"client_secret_basic",
	"client_secret_jwt",
}

// FetchClientMetadataDocument fetches and validates a Client ID Metadata Document
// from the given URL. The URL must use the HTTPS scheme (http://localhost is
// accepted in development). The document is fetched with a 5-second timeout, a
// 1-hop redirect limit, a 10 KB body cap, and SSRF protection via a per-dial IP
// check. After fetching, ValidateClientMetadataDocument is called and any
// validation error is returned.
func FetchClientMetadataDocument(ctx context.Context, rawURL string) (*ClientMetadataDocument, error) {
	if err := validateCIMDClientURL(rawURL); err != nil {
		return nil, err
	}

	result, err := networking.FetchJSON[ClientMetadataDocument](
		ctx,
		newCIMDHTTPClient(),
		rawURL,
		networking.WithMaxResponseSize(10*1024),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch client metadata document: %w", err)
	}

	doc := &result.Data
	if err := ValidateClientMetadataDocument(doc, rawURL); err != nil {
		return nil, err
	}

	return doc, nil
}

// newCIMDHTTPClient builds an *http.Client for fetching CIMD documents.
// Keep-alive connections are disabled so the per-dial SSRF check fires on
// every request. A custom DialContext allows loopback connections (for
// development) while rejecting all other private/special-use ranges via
// networking.IsPrivateIP, and dials by IP literal to prevent DNS-rebinding.
// A CheckRedirect hook validates redirect targets and enforces the 1-hop limit.
//
// HttpClientBuilder.WithPrivateIPs(false) is not used here because it also
// blocks loopback addresses (127.0.0.0/8), which breaks the intentional
// http://localhost development exception in validateCIMDClientURL.
func newCIMDHTTPClient() *http.Client {
	transport := &http.Transport{
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("cimd: invalid dial address %q: %w", address, err)
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("cimd: failed to resolve %q: %w", host, err)
			}
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					continue
				}
				// Loopback is gated by the URL scheme check (only http://localhost
				// is permitted for HTTP). All other private/special-use ranges are
				// rejected. Dial by IP literal to prevent DNS-rebinding TOCTOU.
				if !ip.IsLoopback() && networking.IsPrivateIP(ip) {
					return nil, fmt.Errorf("cimd: refusing connection to private address %s", ipStr)
				}
				dialer := &net.Dialer{Timeout: 5 * time.Second}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ipStr, port))
			}
			return nil, fmt.Errorf("cimd: no valid address found for %q", host)
		},
	}

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
		// CIMD §4: "The authorization server MUST NOT automatically follow
		// HTTP redirects when retrieving the client registration information."
		// Return the redirect response unchanged; FetchJSON will surface it
		// as a non-200 error.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return client
}

// validateCIMDClientURL enforces the client_id URL requirements from
// draft-ietf-oauth-client-id-metadata-document §3:
//   - MUST use https scheme (http://localhost permitted for development only)
//   - MUST contain a non-empty path component (not just "/")
//   - MUST NOT contain a fragment component
//   - MUST NOT contain userinfo (username or password)
//   - MUST NOT contain single-dot or double-dot path segments
func validateCIMDClientURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("client_id URL is not a valid URL: %w", err)
	}
	isLoopback := parsed.Scheme == "http" && oauthproto.IsLoopbackHost(parsed.Hostname())
	if parsed.Scheme != "https" && !isLoopback {
		return fmt.Errorf("client_id URL must use the https scheme: %s", rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("client_id URL must contain a host")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("client_id URL must not contain a fragment component")
	}
	if parsed.User != nil {
		return fmt.Errorf("client_id URL must not contain userinfo (username or password)")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("client_id URL must contain a non-empty path component")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("client_id URL must not contain dot-segment path components")
		}
	}
	return nil
}

// ValidateClientMetadataDocument validates a parsed ClientMetadataDocument
// against the URL it was fetched from. Per the CIMD draft spec, the
// client_id field must exactly equal the URL — no normalization is applied,
// because allowing normalization would permit subtle spoofing attacks where a
// document at URL A claims the identity of URL B.
func ValidateClientMetadataDocument(doc *ClientMetadataDocument, fetchedFrom string) error {
	if doc.ClientID == "" {
		return fmt.Errorf("client metadata document missing required field: client_id")
	}

	if len(doc.RedirectURIs) == 0 {
		return fmt.Errorf("client metadata document missing required field: redirect_uris")
	}

	if doc.ClientID != fetchedFrom {
		return fmt.Errorf("client_id %q does not match the URL it was fetched from %q", doc.ClientID, fetchedFrom)
	}

	for _, uri := range doc.RedirectURIs {
		if err := oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyStrict); err != nil {
			return err
		}
	}

	// Only symmetric shared-secret auth methods are forbidden; asymmetric methods
	// (e.g. private_key_jwt, tls_client_auth, none) are permitted by the spec.
	if doc.TokenEndpointAuthMethod != "" {
		for _, forbidden := range forbiddenAuthMethods {
			if doc.TokenEndpointAuthMethod == forbidden {
				return fmt.Errorf("token_endpoint_auth_method %q is not permitted in a CIMD document: "+
					"symmetric shared-secret methods cannot be used without pre-registration", forbidden)
			}
		}
	}

	return nil
}
