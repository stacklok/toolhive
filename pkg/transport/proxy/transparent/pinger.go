// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides MCP ping functionality for transparent proxies.
package transparent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/networking"
)

// MCPPinger implements healthcheck.MCPPinger for transparent proxies
type MCPPinger struct {
	targetURL string
	// redactedTarget is targetURL reduced to scheme and host, for logs and
	// returned errors. See redactTargetURL.
	redactedTarget string
	client         *http.Client
}

const (
	// DefaultPingerTimeout is the default timeout for health check pings
	DefaultPingerTimeout = 5 * time.Second
)

// redirectPolicy is installed on both pinger clients. It refuses redirects to a
// different host (or an HTTPS-to-HTTP downgrade) rather than following Go's
// http.Client default of any host, any number of hosts, indefinitely. See
// networking.SameHostRedirectPolicy's doc comment for the full rationale; this
// package's data-plane forwarder (followRedirects, in transparent_proxy.go)
// enforces the same same-host-only policy and the two must stay in sync.
var redirectPolicy = networking.SameHostRedirectPolicy()

// NewMCPPingerWithTimeout creates a new MCP pinger with a custom timeout
//
// The configured MCP endpoint may be a remote server outside our control, so
// the client restricts redirects with SameHostRedirectPolicy: without it, a
// malicious or compromised remote could return a 3xx pointing this GET at a
// loopback, private, or otherwise host-reachable address, and the default
// http.Client would follow it (CWE-918, blind SSRF).
func NewMCPPingerWithTimeout(targetURL string, timeout time.Duration) healthcheck.MCPPinger {
	if timeout <= 0 {
		timeout = DefaultPingerTimeout
	}
	return &MCPPinger{
		targetURL:      targetURL,
		redactedTarget: redactTargetURL(targetURL),
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: redirectPolicy,
		},
	}
}

// Ping performs a simple HTTP health check for SSE transport servers
// For SSE transport, we don't send MCP ping requests because that would require
// establishing an SSE session first. Instead, we do a simple HTTP GET to check
// if the server is responding.
func (p *MCPPinger) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()

	// Create a simple GET request to check if the server is responding
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.targetURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create HTTP request: %w", redactURLError(err, p.redactedTarget))
	}

	//nolint:gosec // G706: logging redacted target URL from config
	slog.Debug("checking SSE server health", "target", p.redactedTarget)

	// Send the request
	resp, err := p.client.Do(req) // #nosec G704 -- targetURL is the configured MCP server health endpoint
	if err != nil {
		return time.Since(start), fmt.Errorf("failed to connect to SSE server: %w", redactURLError(err, p.redactedTarget))
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close response body", "error", err)
		}
	}()

	duration := time.Since(start)

	// For Streamable HTTP servers, we expect various responses:
	// - 200 OK for root endpoints
	// - 404 for non-existent endpoints (but server is still alive)
	// - 401 for remote workloads (we may not be able to authenticate, but this response indicates the server is alive).
	// - Other 4xx/5xx may indicate server issues
	// For now, we accept any non 50x response for both local and remote.
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		//nolint:gosec // G706: logging HTTP status code from health check response
		slog.Debug("sse server health check successful",
			"duration", duration, "status", resp.StatusCode)
		return duration, nil
	}

	return duration, fmt.Errorf("SSE server health check failed with status %d", resp.StatusCode)
}

// StatelessMCPPinger health-checks stateless streamable-HTTP servers via POST ping.
// Stateless servers don't support GET, so we send a minimal JSON-RPC ping instead.
type StatelessMCPPinger struct {
	targetURL string
	// redactedTarget is targetURL reduced to scheme and host, for logs and
	// returned errors. See redactTargetURL.
	redactedTarget string
	client         *http.Client
}

// NewStatelessMCPPingerWithTimeout creates a pinger for stateless
// streamable-HTTP servers with a custom timeout. A non-positive timeout selects
// DefaultPingerTimeout.
//
// Same redirect restriction as NewMCPPingerWithTimeout, and for the same
// reason: the target may be an untrusted remote endpoint. A 307/308 here would
// otherwise carry the POST body and method to whatever host the response
// names; 301/302/303 already lose the body under Go's default redirect
// handling, so refusing cross-host keeps that residual GET on the configured
// host too.
func NewStatelessMCPPingerWithTimeout(targetURL string, timeout time.Duration) healthcheck.MCPPinger {
	if timeout <= 0 {
		timeout = DefaultPingerTimeout
	}
	return &StatelessMCPPinger{
		targetURL:      targetURL,
		redactedTarget: redactTargetURL(targetURL),
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: redirectPolicy,
		},
	}
}

// Ping sends a JSON-RPC ping POST to check if the stateless server is reachable.
// Accepts any 2xx-4xx response as healthy; only network errors and 5xx indicate failure.
func (p *StatelessMCPPinger) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()

	body := `{"jsonrpc":"2.0","id":0,"method":"ping","params":{}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.targetURL, strings.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create stateless ping request: %w", redactURLError(err, p.redactedTarget))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	//nolint:gosec // G706: logging redacted target URL from config
	slog.Debug("checking stateless MCP server health via POST ping", "target", p.redactedTarget)

	resp, err := p.client.Do(req)
	if err != nil {
		return time.Since(start), fmt.Errorf("stateless ping failed to connect: %w", redactURLError(err, p.redactedTarget))
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close ping response body", "error", err)
		}
	}()

	duration := time.Since(start)

	// Accept 2xx-4xx: even 401/403 means the server is reachable.
	// Only 5xx or network errors indicate the server is down.
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		//nolint:gosec // G706: logging HTTP status code from health check response
		slog.Debug("stateless MCP server health check successful",
			"duration", duration, "status", resp.StatusCode)
		return duration, nil
	}

	return duration, fmt.Errorf("stateless ping returned status %d", resp.StatusCode)
}

// redactedTargetPlaceholder stands in for a target URL that does not parse to
// a scheme and host. The raw string is never used in its place, since a URL
// that fails to parse can still carry a secret.
const redactedTargetPlaceholder = "<unparsable target URL>"

// errUnparsableRedirectLocation replaces net/http's error for a redirect whose
// Location header does not parse. That error's text quotes the raw Location,
// which on a same-host canonicalization redirect commonly repeats the target's
// path and query.
var errUnparsableRedirectLocation = errors.New("failed to parse redirect Location header")

// redactTargetURL reduces a pinger target to its scheme and host (including
// any port), e.g. "https://host:8443".
//
// Remote MCP URLs often carry credentials in the path or query (an API key in
// "?api_key=..."). Ping errors reach the unauthenticated /health endpoint via
// healthcheck.MCPStatus.Error and are logged on every check, so neither may
// include more of the target than this.
func redactTargetURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return redactedTargetPlaceholder
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// redactURLError rebuilds the *url.Error that net/http returns from
// http.NewRequest and http.Client.Do with redactedTarget in place of its URL.
// net/http strips only a userinfo password from that URL, so the original
// carries the target's full path and query; after a redirect it carries the
// Location instead. The inner error is kept, so errors.Is still matches
// context.DeadlineExceeded and networking.ErrRedirectRefused, and the result is
// still a *url.Error, so its Timeout method keeps working.
//
// The inner error is kept verbatim except for net/http's Location-parse
// failure, which quotes the raw Location. The other inner errors these pingers
// can see do not include a path or query: transport and dial errors name at
// most the host and port, and SameHostRedirectPolicy's refusals name only the
// redirect host or scheme.
func redactURLError(err error, redactedTarget string) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	inner := urlErr.Err
	// net/http builds this error with fmt.Errorf("failed to parse Location
	// header %q: %v", ...), so it has no chain to preserve and can only be
	// recognized by its text.
	if inner != nil && strings.HasPrefix(inner.Error(), "failed to parse Location header") {
		inner = errUnparsableRedirectLocation
	}
	return &url.Error{Op: urlErr.Op, URL: redactedTarget, Err: inner}
}
