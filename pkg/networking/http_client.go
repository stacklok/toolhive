// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
)

// HTTPClient is an interface for making HTTP requests.
// This interface is satisfied by *http.Client and allows for dependency injection in testing.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

var privateIPBlocks []*net.IPNet

// HttpTimeout is the timeout for outgoing HTTP requests
const HttpTimeout = 30 * time.Second

// HttpsScheme is the HTTPS scheme
const HttpsScheme = "https"

// HttpScheme is the HTTP scheme
const HttpScheme = "http"

// MaxRedirects bounds how many HTTP redirects an SSRF-guarded client follows
// before giving up. Matches the cap used by the transparent proxy data path.
const MaxRedirects = 10

// ErrRedirectRefused is wrapped by SameHostRedirectPolicy when it declines to
// follow a redirect, so callers can match it with errors.Is.
var ErrRedirectRefused = errors.New("redirect refused")

// SameHostRedirectPolicy returns a value for http.Client.CheckRedirect that
// follows only same-host redirects, refuses HTTPS-to-HTTP downgrades, and caps
// the chain at MaxRedirects.
//
// Any client that fetches a URL derived from an untrusted remote server — auth
// discovery probes, RFC 9728 resource-metadata fetches, OIDC issuer discovery —
// must install this. Validating only the originally-supplied URL is not enough:
// a malicious server can return a 30x that points the request at an internal
// address (cloud IMDS, RFC1918 services), and the host-side client would follow
// it (CWE-918). Restricting redirects to the same host as the original request
// keeps the request on the endpoint the operator actually configured.
//
// This mirrors the data-plane guard in pkg/transport/proxy/transparent
// (followRedirects); keep the two policies in sync.
func SameHostRedirectPolicy() func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return fmt.Errorf("stopped after %d redirects: %w", MaxRedirects, ErrRedirectRefused)
		}
		// via[0] is the original request; CheckRedirect is only invoked once at
		// least one redirect has occurred, so via is never empty.
		original := via[0]
		// Compare host:port (not just hostname): a redirect to a different port
		// on the same host can reach a different internal service (a co-located
		// metadata/admin port), so it must be treated as cross-host.
		if !strings.EqualFold(req.URL.Host, original.URL.Host) {
			return fmt.Errorf("refusing cross-host redirect to %q (original host %q): %w",
				req.URL.Host, original.URL.Host, ErrRedirectRefused)
		}
		if original.URL.Scheme == HttpsScheme && req.URL.Scheme != HttpsScheme {
			return fmt.Errorf("refusing redirect that downgrades from HTTPS to %q: %w",
				req.URL.Scheme, ErrRedirectRefused)
		}
		return nil
	}
}

// Dialer control function for validating addresses prior to connection
func protectedDialerControl(_, address string, _ syscall.RawConn) error {
	err := AddressReferencesPrivateIp(address)
	if err != nil {
		return err
	}

	return nil
}

// NewPrivateIPBlockingDialContext returns a DialContext that refuses to connect
// to private, loopback, or link-local addresses. The check runs after DNS
// resolution on the address actually being dialed, so it also defends against
// DNS rebinding and is re-applied on every redirect hop. Pair it with
// Transport.DisableKeepAlives so a pooled connection cannot skip the check on a
// later request.
//
// Use this on clients that fetch a URL derived from untrusted input when the
// operator-configured target is public; SameHostRedirectPolicy is the
// redirect-following counterpart.
func NewPrivateIPBlockingDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Control: protectedDialerControl}).DialContext
}

// ValidatingTransport is for validating URLs prior to request
type ValidatingTransport struct {
	Transport         http.RoundTripper
	InsecureAllowHTTP bool
}

// RoundTrip validates the request URL prior to forwarding
func (t *ValidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Skip validation if INSECURE_DISABLE_URL_VALIDATION is set or if InsecureAllowHTTP is true
	if strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true") || t.InsecureAllowHTTP {
		return t.Transport.RoundTrip(req)
	}

	// Check for valid URL specification
	parsedUrl, err := url.Parse(req.URL.String())
	if err != nil {
		return nil, fmt.Errorf("the supplied URL %s is malformed", req.URL.String())
	}

	// Check for HTTPS scheme
	if parsedUrl.Scheme != HttpsScheme {
		return nil, fmt.Errorf("the supplied URL %s is not HTTPS scheme", req.URL.String())
	}

	return t.Transport.RoundTrip(req)
}

// createTokenSourceFromFile creates an oauth2.TokenSource from a token file
func createTokenSourceFromFile(tokenFile string) (oauth2.TokenSource, error) {
	tokenBytes, err := os.ReadFile(tokenFile) // #nosec G304 - tokenFile path is provided by user via CLI flag
	if err != nil {
		return nil, fmt.Errorf("failed to read auth token file: %w", err)
	}

	// Remove any trailing newlines/whitespace
	tokenStr := strings.TrimSpace(string(tokenBytes))
	if tokenStr == "" {
		return nil, fmt.Errorf("auth token file is empty")
	}

	// Create a static token source
	token := &oauth2.Token{
		AccessToken: tokenStr,
		TokenType:   "Bearer",
	}

	return oauth2.StaticTokenSource(token), nil
}

// HttpClientBuilder provides a fluent interface for building HTTP clients
type HttpClientBuilder struct {
	clientTimeout         time.Duration
	tlsHandshakeTimeout   time.Duration
	responseHeaderTimeout time.Duration
	caCertPath            string
	authTokenFile         string
	allowPrivate          bool
	insecureAllowHTTP     bool
	disableKeepAlives     bool
}

// NewHttpClientBuilder returns a new HttpClientBuilder
func NewHttpClientBuilder() *HttpClientBuilder {
	return &HttpClientBuilder{
		clientTimeout:         HttpTimeout,
		tlsHandshakeTimeout:   10 * time.Second,
		responseHeaderTimeout: 10 * time.Second,
	}
}

// NewHostScopedClientBuilder returns an HttpClientBuilder pre-configured with
// the SSRF-guard policy (CWE-918) appropriate for dialing host. By default the
// returned builder blocks plain HTTP and connections to private/loopback/
// link-local IP ranges. Both gates are relaxed automatically for loopback
// hosts (development/testing) and when INSECURE_DISABLE_URL_VALIDATION is set.
//
// allowPrivateIPs widens only the private-IP gate — for example an in-cluster
// provider reachable solely over an RFC-1918 address — without enabling plain
// HTTP for non-loopback hosts. insecureAllowHTTP additionally permits
// plain-HTTP for non-loopback hosts and must never be set in production.
//
// The returned builder is not yet built: callers may chain further options
// (e.g. WithTimeout, WithDisableKeepAlives) before calling Build. This is the
// single source of truth for the host-scoped guard policy shared by the
// upstream OAuth2/OIDC providers and the DCR resolver so the two paths cannot
// drift.
func NewHostScopedClientBuilder(host string, allowPrivateIPs, insecureAllowHTTP bool) *HttpClientBuilder {
	allowInsecure := IsLocalhost(host) ||
		insecureAllowHTTP ||
		strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true")
	return NewHttpClientBuilder().
		WithInsecureAllowHTTP(allowInsecure).
		WithPrivateIPs(allowInsecure || allowPrivateIPs)
}

// WithCABundle sets the CA certificate bundle path
func (b *HttpClientBuilder) WithCABundle(path string) *HttpClientBuilder {
	b.caCertPath = path
	return b
}

// WithTokenFromFile sets the auth token file path
func (b *HttpClientBuilder) WithTokenFromFile(path string) *HttpClientBuilder {
	b.authTokenFile = path
	return b
}

// WithPrivateIPs allows connections to private IP addresses
func (b *HttpClientBuilder) WithPrivateIPs(allow bool) *HttpClientBuilder {
	b.allowPrivate = allow
	return b
}

// WithInsecureAllowHTTP allows HTTP (non-HTTPS) URLs
// WARNING: This is insecure and should NEVER be used in production
func (b *HttpClientBuilder) WithInsecureAllowHTTP(allow bool) *HttpClientBuilder {
	b.insecureAllowHTTP = allow
	return b
}

// WithDisableKeepAlives disables HTTP keep-alive on the transport. When true,
// each request uses a fresh connection, ensuring the per-dial SSRF check fires
// on every request rather than being bypassed by a reused connection.
func (b *HttpClientBuilder) WithDisableKeepAlives(disable bool) *HttpClientBuilder {
	b.disableKeepAlives = disable
	return b
}

// WithTimeout sets the HTTP client timeout
func (b *HttpClientBuilder) WithTimeout(timeout time.Duration) *HttpClientBuilder {
	b.clientTimeout = timeout
	return b
}

// Build creates the configured HTTP client
func (b *HttpClientBuilder) Build() (*http.Client, error) {
	transport := &http.Transport{
		TLSHandshakeTimeout:   b.tlsHandshakeTimeout,
		ResponseHeaderTimeout: b.responseHeaderTimeout,
	}
	transport.DisableKeepAlives = b.disableKeepAlives

	if !b.allowPrivate {
		transport.DialContext = (&net.Dialer{
			Control: protectedDialerControl,
		}).DialContext
	}

	if b.caCertPath != "" {
		caCert, err := os.ReadFile(b.caCertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate bundle: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate bundle")
		}

		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
		transport.TLSClientConfig.RootCAs = caCertPool
	}

	// Start with validation transport
	var clientTransport http.RoundTripper = &ValidatingTransport{
		Transport:         transport,
		InsecureAllowHTTP: b.insecureAllowHTTP,
	}

	// Add auth transport if token file is provided using oauth2.Transport
	if b.authTokenFile != "" {
		tokenSource, err := createTokenSourceFromFile(b.authTokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to create token source: %w", err)
		}

		// oauth2.Transport wraps our existing transport and adds Bearer token authentication
		clientTransport = &oauth2.Transport{
			Source: tokenSource,
			Base:   clientTransport, // Preserves our ValidatingTransport
		}
	}

	client := &http.Client{
		Transport: clientTransport,
		Timeout:   b.clientTimeout,
	}

	return client, nil
}
