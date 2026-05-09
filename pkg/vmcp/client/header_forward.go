// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
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
// delegates to the wrapped transport. Headers already present on the request
// are left untouched so inner transports (auth, identity, trace) can always
// override user-supplied values for the same name.
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

// buildHeaderForwardTripper constructs a headerForwardRoundTripper for the
// backend's pre-resolved HeaderForwardConfig. Returns base unchanged when no
// header injection is configured or the effective header set is empty.
//
// Fails loudly (constructor validation, per go-style.md) when a secret identifier
// cannot be resolved through the provider, so a misconfigured backend surfaces
// at pod startup — not as a silent missing-header on every request.
//
// Restricted header names (matching pkg/transport/middleware.RestrictedHeaders)
// are rejected to prevent Host, Content-Length, Authorization, hop-by-hop, and
// X-Forwarded-* spoofing via user-supplied config.
func buildHeaderForwardTripper(
	base http.RoundTripper,
	cfg *vmcp.HeaderForwardConfig,
	provider secrets.Provider,
	backendName string,
) (http.RoundTripper, error) {
	if cfg == nil {
		return base, nil
	}

	headers, err := resolveHeaderForward(cfg, provider, backendName)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return base, nil
	}
	return &headerForwardRoundTripper{base: base, headers: headers}, nil
}

// resolveHeaderForward merges plaintext headers with secret-resolved headers into
// a single canonicalized http.Header. Plaintext entries are copied verbatim;
// secret identifiers are looked up via the provider (TOOLHIVE_SECRET_<ident>).
//
// Returns an error if any header name is in middleware.RestrictedHeaders or if
// any referenced secret identifier is not present in the environment.
func resolveHeaderForward(
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
		value, err := provider.GetSecret(nil, identifier) //nolint:staticcheck // provider ignores ctx
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
