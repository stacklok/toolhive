// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package headerforward provides the HTTP round-tripper that injects
// per-backend forwarded headers (plaintext + secret-resolved) onto outbound
// requests. It is consumed by both the capability-discovery HTTP client
// (pkg/vmcp/client) and the per-session HTTP client
// (pkg/vmcp/session/internal/backend).
package headerforward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// headerForwardRoundTripper injects a pre-resolved set of HTTP headers onto every
// outbound request to a specific backend. Headers are resolved once at client
// creation time (plaintext verbatim + secret identifiers looked up via
// secrets.EnvironmentProvider) and stored in an immutable map. The request is
// cloned before mutation so callers retain an untouched request.
//
// This tripper applies to health checks, listTools/listResources/listPrompts, and
// tool/resource/prompt invocations — every HTTP call made by the vMCP backend
// client passes through the same transport chain.
type headerForwardRoundTripper struct {
	base    http.RoundTripper
	headers http.Header // canonicalized header name → single value
}

// RoundTrip injects the pre-resolved headers onto a clone of the request and
// delegates to the wrapped transport. Names already present on the inbound
// request are skipped — this preserves any header the *caller* set on the
// request before invoking the round-tripper chain. Inner (closer to the wire)
// transports run AFTER this one and use Set() unconditionally, so on any
// overlapping name those stages still win on the wire. Restricted names are
// blocked at resolve time, so user-supplied config cannot reach this point
// for Host, hop-by-hop, or X-Forwarded-* anyway.
func (h *headerForwardRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(h.headers) == 0 {
		return h.base.RoundTrip(req)
	}
	reqCopy := req.Clone(req.Context())
	if reqCopy.Header == nil {
		reqCopy.Header = make(http.Header)
	}
	for name, values := range h.headers {
		if len(values) == 0 {
			continue
		}
		// Do not clobber headers already set by earlier (outer) tripper stages.
		// The vMCP runtime is authoritative for identity/trace/auth headers.
		if reqCopy.Header.Get(name) != "" {
			continue
		}
		reqCopy.Header.Set(name, values[0])
	}
	return h.base.RoundTrip(reqCopy)
}

// CloseIdleConnections forwards to the wrapped RoundTripper so http.Client's
// CloseIdleConnections reaches the concrete *http.Transport at the bottom of the
// chain (this wrapper would otherwise silently swallow the call).
func (h *headerForwardRoundTripper) CloseIdleConnections() {
	if c, ok := h.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// BuildHeaderForwardTripper constructs a headerForwardRoundTripper for the
// backend's pre-resolved HeaderForwardConfig. Returns base unchanged when no
// header injection is configured or the effective header set is empty.
//
// Used by both the vMCP backend client (startup capability discovery) and the
// per-session backend connector (long-lived MCP traffic). Exported so the
// session backend in pkg/vmcp/session/internal/backend can share the same
// transport-chain wiring.
//
// Fails loudly (constructor validation, per go-style.md) when a secret identifier
// cannot be resolved through the provider, so a misconfigured backend surfaces
// at pod startup — not as a silent missing-header on every request.
//
// Restricted header names (matching pkg/transport/middleware.RestrictedHeaders)
// are rejected to prevent Host, Content-Length, Authorization, hop-by-hop, and
// X-Forwarded-* spoofing via user-supplied config.
func BuildHeaderForwardTripper(
	ctx context.Context,
	base http.RoundTripper,
	cfg *vmcp.HeaderForwardConfig,
	provider secrets.Provider,
	backendName string,
) (http.RoundTripper, error) {
	if cfg == nil {
		return base, nil
	}

	headers, err := resolveHeaderForward(ctx, cfg, provider, backendName)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return base, nil
	}
	return &headerForwardRoundTripper{base: base, headers: headers}, nil
}

// MergeForwardedHeaders returns a HeaderForwardConfig that combines the static
// backend configuration (base) with any per-request forwarded headers captured
// from the caller's request (see CaptureMiddleware / ForwardedHeadersFromContext).
//
// Rules (applied in order):
//  1. If forwarded is empty, base is returned unchanged (no allocation, same pointer).
//  2. A new HeaderForwardConfig is built from a shallow copy of base so the
//     shared target.HeaderForward is never mutated.
//  3. All output AddPlaintextHeaders keys (both static and forwarded) are
//     canonicalized via http.CanonicalHeaderKey. Static keys from
//     base.AddPlaintextHeaders are therefore also re-canonicalized in the output,
//     which changes the intermediate map representation (though wire behavior is
//     identical since resolveHeaderForward / BuildHeaderForwardTripper
//     canonicalize again before writing to http.Header).
//  4. Forwarded header names are additionally checked against
//     middleware.RestrictedHeaders; restricted names are silently dropped
//     (defense-in-depth — they were already filtered upstream by
//     CaptureMiddleware, but we guard here too).
//  5. A forwarded header name that also appears in base (AddPlaintextHeaders or
//     AddHeadersFromSecret) is a misconfiguration: the function returns an error
//     rather than silently picking a winner.
func MergeForwardedHeaders(base *vmcp.HeaderForwardConfig, forwarded map[string]string) (*vmcp.HeaderForwardConfig, error) {
	if len(forwarded) == 0 {
		return base, nil
	}

	// Build the merged AddPlaintextHeaders starting from the static config.
	var staticPlaintext map[string]string
	if base != nil {
		staticPlaintext = base.AddPlaintextHeaders
	}

	// Canonical set of header names already owned by the static config, used for
	// collision detection.
	staticNames := make(map[string]struct{})
	if base != nil {
		for k := range base.AddPlaintextHeaders {
			staticNames[http.CanonicalHeaderKey(k)] = struct{}{}
		}
		for k := range base.AddHeadersFromSecret {
			staticNames[http.CanonicalHeaderKey(k)] = struct{}{}
		}
	}

	merged := make(map[string]string, len(staticPlaintext)+len(forwarded))
	for k, v := range staticPlaintext {
		// Canonicalize static keys (consistent with forwarded keys below) so the
		// returned map has uniformly canonical HTTP header names regardless of how
		// the static config was specified.
		merged[http.CanonicalHeaderKey(k)] = v
	}

	for name, value := range forwarded {
		canonical := http.CanonicalHeaderKey(name)
		if middleware.RestrictedHeaders[canonical] {
			slog.Debug("dropping restricted forwarded header", "header", canonical)
			continue
		}
		if _, exists := staticNames[canonical]; exists {
			return nil, fmt.Errorf(
				"forwarded header %q collides with the backend's static header-forward config", canonical)
		}
		merged[canonical] = value
	}

	out := &vmcp.HeaderForwardConfig{
		AddPlaintextHeaders: merged,
	}
	if base != nil {
		out.AddHeadersFromSecret = base.AddHeadersFromSecret
	}
	return out, nil
}

// resolveHeaderForward merges plaintext headers with secret-resolved headers into
// a single canonicalized http.Header. Plaintext entries are copied verbatim;
// secret identifiers are looked up via the provider (TOOLHIVE_SECRET_<ident>).
//
// Returns an error if any header name is in middleware.RestrictedHeaders or if
// any referenced secret identifier is not present in the environment.
func resolveHeaderForward(
	ctx context.Context,
	cfg *vmcp.HeaderForwardConfig,
	provider secrets.Provider,
	backendName string,
) (http.Header, error) {
	if cfg == nil {
		return nil, nil
	}

	size := len(cfg.AddPlaintextHeaders) + len(cfg.AddHeadersFromSecret)
	if size == 0 {
		return nil, nil
	}
	out := make(http.Header, size)

	for name, value := range cfg.AddPlaintextHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if middleware.RestrictedHeaders[canonical] {
			return nil, fmt.Errorf(
				"backend %q: header %q is restricted and cannot be forwarded",
				backendName, canonical,
			)
		}
		out.Set(canonical, value)
	}

	if provider == nil && len(cfg.AddHeadersFromSecret) > 0 {
		return nil, fmt.Errorf(
			"backend %q: secret provider required to resolve %d header secret(s)",
			backendName, len(cfg.AddHeadersFromSecret),
		)
	}

	for name, identifier := range cfg.AddHeadersFromSecret {
		canonical := http.CanonicalHeaderKey(name)
		if middleware.RestrictedHeaders[canonical] {
			return nil, fmt.Errorf(
				"backend %q: header %q is restricted and cannot be forwarded",
				backendName, canonical,
			)
		}
		value, err := provider.GetSecret(ctx, identifier)
		if err != nil {
			// Do not include the identifier or value in any user-facing error;
			// the log is the only place we record the identifier, not the value.
			slog.Error("failed to resolve header-forward secret",
				"backend", backendName, "header", canonical, "identifier", identifier,
				"error", err)
			if errors.Is(err, secrets.ErrSecretNotFound) {
				return nil, fmt.Errorf(
					"backend %q: header %q secret not found in pod environment",
					backendName, canonical,
				)
			}
			return nil, fmt.Errorf(
				"backend %q: failed to resolve header %q from secret provider: %w",
				backendName, canonical, err,
			)
		}
		out.Set(canonical, value)
	}

	return out, nil
}
