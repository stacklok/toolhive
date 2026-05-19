// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ToolHiveClientMetadataDocumentURL is the stable HTTPS URL where ToolHive's
// client metadata document is hosted. ToolHive presents this URL as its
// client_id to remote authorization servers that support CIMD. The URL must
// be live and serving the client metadata document before this feature can
// be used in production.
const ToolHiveClientMetadataDocumentURL = "https://toolhive.dev/oauth/client-metadata.json"

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

// cimdPrivateBlocks holds the IP ranges that the CIMD fetcher must refuse to
// connect to. This list mirrors pkg/networking.privateIPBlocks but is
// maintained here to preserve the leaf-package invariant — oauthproto must
// not import pkg/networking (see doc.go).
var cimdPrivateBlocks []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",     // IPv4 loopback
		"10.0.0.0/8",      // RFC1918
		"172.16.0.0/12",   // RFC1918
		"192.168.0.0/16",  // RFC1918
		"169.254.0.0/16",  // RFC3927 link-local
		"::1/128",         // IPv6 loopback
		"fe80::/10",       // IPv6 link-local
		"fc00::/7",        // IPv6 unique local addr
		"100.64.0.0/10",   // RFC6598 shared address space (CGN)
		"192.0.2.0/24",    // RFC5737 documentation (TEST-NET-1)
		"198.51.100.0/24", // RFC5737 documentation (TEST-NET-2)
		"203.0.113.0/24",  // RFC5737 documentation (TEST-NET-3)
		"224.0.0.0/4",     // IPv4 multicast
		"ff00::/8",        // IPv6 multicast
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("cimd: parse error on CIDR %q: %w", cidr, err))
		}
		cimdPrivateBlocks = append(cimdPrivateBlocks, block)
	}
}

// isPrivateForCIMD reports whether ip falls within any SSRF-blocked range.
func isPrivateForCIMD(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, block := range cimdPrivateBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// IsClientIDMetadataDocumentURL returns true if clientID is an HTTPS URL.
// Any HTTPS URL is treated as a CIMD client_id; DCR-issued IDs are always
// opaque strings that never begin with "https://". Do not tighten this to an
// exact match against ToolHiveClientMetadataDocumentURL — the embedded AS
// must accept CIMD URLs from third-party clients too.
func IsClientIDMetadataDocumentURL(clientID string) bool {
	return strings.HasPrefix(clientID, "https://")
}

// validateCIMDClientURL enforces the client_id URL requirements from
// draft-ietf-oauth-client-id-metadata-document §3:
//   - MUST use https scheme (http://localhost permitted for development only)
//   - MUST contain a non-empty path component (not just "/")
//   - MUST NOT contain a fragment component
//   - MUST NOT contain userinfo (username or password)
//   - MUST NOT contain single-dot or double-dot path segments
func validateCIMDClientURL(rawURL string) error {
	isLoopback := isLoopbackHTTP(rawURL)
	if !strings.HasPrefix(rawURL, "https://") && !isLoopback {
		return fmt.Errorf("client_id URL must use the https scheme: %s", rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("client_id URL is not a valid URL: %w", err)
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

// isLoopbackHTTP reports whether rawURL is an HTTP URL targeting a loopback
// address. The host is parsed from the URL (not matched as a prefix) to
// prevent bypasses via hosts like "http://localhost.evil.com/".
func isLoopbackHTTP(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname() // strips port
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// FetchClientMetadataDocument fetches and validates a Client ID Metadata Document
// from the given URL. The URL must use the HTTPS scheme (http://localhost is
// accepted in tests). The document is fetched with a 5-second timeout, a 1-hop
// redirect limit, a 10 KB body cap, and SSRF protection via a per-dial IP
// check. After fetching, ValidateClientMetadataDocument is called and any
// validation error is returned.
func FetchClientMetadataDocument(ctx context.Context, rawURL string) (*ClientMetadataDocument, error) {
	if err := validateCIMDClientURL(rawURL); err != nil {
		return nil, err
	}

	httpClient := newCIMDHTTPClient()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch client metadata document: %w", err)
	}
	// Keep-alive connections are disabled so no drain is needed to allow connection reuse.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client metadata document fetch returned HTTP %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	mediaType, _, parseErr := mime.ParseMediaType(ct)
	if parseErr != nil || (mediaType != "application/json" && !isJSONSubtype(mediaType)) {
		return nil, fmt.Errorf("client metadata document has unexpected Content-Type: %q", ct)
	}

	const maxBodyBytes = 10 * 1024
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	var doc ClientMetadataDocument
	if err := json.NewDecoder(limited).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode client metadata document: %w", err)
	}

	if err := ValidateClientMetadataDocument(&doc, rawURL); err != nil {
		return nil, err
	}

	return &doc, nil
}

// newCIMDHTTPClient builds an *http.Client with the SSRF and redirect guards
// required for fetching client metadata documents. Keep-alive connections are
// disabled so the per-dial SSRF check fires on every request rather than being
// bypassed by a reused connection.
func newCIMDHTTPClient() *http.Client {
	transport := &http.Transport{
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableKeepAlives:     true,
		// DialContext resolves the hostname, validates every resolved IP against
		// the SSRF block list, then dials by IP literal to prevent DNS-rebinding
		// attacks (TOCTOU between resolution and dial).
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("cimd: invalid dial address %q: %w", address, err)
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("cimd: failed to resolve %q: %w", host, err)
			}
			// Pick the first IP that passes SSRF validation and dial it directly
			// by literal to eliminate the TOCTOU window between resolution and dial.
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					continue
				}
				// Loopback is allowed — gated by the URL scheme check above.
				// All other private/special-use ranges are rejected.
				if !ip.IsLoopback() && isPrivateForCIMD(ip) {
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
		// Validate each redirect target before following it. A HTTPS URL could
		// redirect to HTTP or to a private IP that bypasses the URL-level checks.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 1 {
				return http.ErrUseLastResponse
			}
			if err := validateCIMDClientURL(req.URL.String()); err != nil {
				return fmt.Errorf("cimd: redirect target rejected: %w", err)
			}
			return nil
		},
	}

	return client
}

// isJSONSubtype reports whether mediaType is an application/*+json subtype.
func isJSONSubtype(mediaType string) bool {
	return strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")
}

// forbiddenAuthMethods lists token_endpoint_auth_method values that MUST NOT
// appear in a CIMD document. Per the spec, CIMD clients may not use symmetric
// shared-secret methods because no pre-shared secret is ever established.
var forbiddenAuthMethods = []string{
	"client_secret_post",
	"client_secret_basic",
	"client_secret_jwt",
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
		if err := validateRedirectURI(uri); err != nil {
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

// validateRedirectURI checks that a redirect URI uses an allowed scheme.
// Loopback HTTP (localhost / 127.0.0.1 / [::1]) and HTTPS are the only
// permitted schemes; all others (http://example.com, ftp://, etc.) are
// rejected to prevent open-redirect and phishing risks.
func validateRedirectURI(uri string) error {
	switch {
	case strings.HasPrefix(uri, "https://"):
		return nil
	case strings.HasPrefix(uri, "http://localhost"),
		strings.HasPrefix(uri, "http://127.0.0.1"),
		strings.HasPrefix(uri, "http://[::1]"):
		return nil
	default:
		return fmt.Errorf("redirect_uri %q must use https:// or a loopback http:// address", uri)
	}
}
