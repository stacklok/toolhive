// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// AuthorizationHeader is the header the injector mutates on allow.
const AuthorizationHeader = "authorization"

// Destination is the request's destination, extracted from the bumped
// request. Host must be port-stripped by the caller.
type Destination struct {
	Host   string
	Method string
	Path   string
}

// Decision is the injector's verdict. On Deny, HeaderName/HeaderValue are
// guaranteed empty: no deny path may ever carry header material.
type Decision struct {
	Allow       bool
	DenyReason  string
	HeaderName  string
	HeaderValue string //nolint:gosec // G117: field legitimately holds sensitive data
}

// CredentialInjector enforces destination binding (ADR-0001 D5) and injects
// the pod-owner's upstream credential.
//
// Ordering is the security invariant and MUST NOT be reordered:
//  1. Provider lookup (which provider's allowlist contains the destination?)
//  2. Method/path policy check
//  3. ONLY THEN the credential load (Strict binding, ExpectedBinding from the
//     pod's own identity — never from request data)
//  4. Header mutation
//
// Every failure mode denies; no deny path touches the token reader or writes
// a header. The token reader must be wrapped in upstreamtoken.NewStrictTokenReader
// by the caller (the constructor does it here so the wrap cannot be forgotten).
type CredentialInjector struct {
	identity    PodIdentity
	policy      *EgressPolicy
	tokenReader upstreamtoken.TokenReader
}

// NewCredentialInjector wires the injector. tokenReader is wrapped with
// NewStrictTokenReader internally so every read fails closed on legacy
// (unowned) token rows. Fails loudly on nil dependencies.
func NewCredentialInjector(
	identity PodIdentity,
	policy *EgressPolicy,
	tokenReader upstreamtoken.TokenReader,
) (*CredentialInjector, error) {
	if policy == nil {
		return nil, fmt.Errorf("egressbroker: policy must not be nil")
	}
	if tokenReader == nil {
		return nil, fmt.Errorf("egressbroker: token reader must not be nil")
	}
	if identity.Issuer == "" || identity.Subject == "" || identity.SessionID == "" || identity.MCPServer == "" {
		return nil, fmt.Errorf("egressbroker: pod identity is incomplete; refusing to build injector")
	}
	return &CredentialInjector{
		identity:    identity,
		policy:      policy,
		tokenReader: upstreamtoken.NewStrictTokenReader(tokenReader),
	}, nil
}

// Evaluate decides whether dest may carry the pod-owner's credential and, if
// so, returns the Authorization header mutation. It never logs credential
// material; deny reasons carry no token data.
func (i *CredentialInjector) Evaluate(ctx context.Context, dest Destination) Decision {
	// D5 step 1: provider lookup — before any credential load.
	provider, ok := i.policy.ProviderFor(dest.Host)
	if !ok {
		return deny("no provider allowlists this destination")
	}
	// D5 step 2: method + path check — still before any credential load.
	if !i.policy.Allows(provider, dest.Method, dest.Path) {
		return deny("method/path outside provider policy")
	}

	// Step 3: credential load, bound to the pod's own identity. The expected
	// binding's UserID is the pod-owner's raw sub from the downward-API
	// registry; Strict (forced by NewStrictTokenReader) fails closed on rows
	// that cannot prove their owner.
	creds, failed, err := i.tokenReader.GetAllUpstreamCredentials(ctx, i.identity.SessionID,
		&storage.ExpectedBinding{UserID: i.identity.Subject, Strict: true})
	if err != nil {
		// Fail closed on storage errors (Redis down, binding violations):
		// never passthrough.
		slog.DebugContext(ctx, "egressbroker: credential load failed; denying",
			"provider", provider, "error", err)
		return deny("upstream credential load failed")
	}
	cred, ok := creds[provider]
	if !ok || slices.Contains(failed, provider) {
		// Re-consent denial: this 403 is the security enforcement backstop for
		// the case where vMCP's fail-fast consent check is bypassed (e.g. a
		// malicious server attempting egress anyway). The agent-facing consent
		// UX (provider + authorize URL) comes from the vMCP upstream_inject
		// path, never from here: the denial surfaces on the server's own
		// outbound HTTPS call, not on the vMCP channel, so no consent URL can
		// or should be threaded through this response.
		return deny("upstream credential unavailable; re-consent required")
	}
	if cred.AccessToken == "" {
		// Defensive: a successful load with an empty token must never produce
		// an "Authorization: Bearer " header.
		return deny("upstream credential unavailable; re-consent required")
	}

	return Decision{
		Allow:       true,
		HeaderName:  AuthorizationHeader,
		HeaderValue: "Bearer " + cred.AccessToken,
	}
}

func deny(reason string) Decision {
	return Decision{Allow: false, DenyReason: reason}
}
