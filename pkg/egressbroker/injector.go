// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"errors"
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

// Result is the injector's verdict plus the structured context the caller
// (ext_authz server) needs for metrics/audit/scan-correlation. On Deny,
// HeaderName/HeaderValue are guaranteed empty: no deny path may ever carry
// header material.
type Result struct {
	Allow       bool
	Reason      DenyReason
	DenyDetail  string // coarse operator-facing text; never token data
	Provider    string // the provider whose policy matched ("" on no-policy deny)
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
	identity PodIdentity
	podName  string
	policy   *EgressPolicy
	reader   upstreamtoken.TokenReader
	tokens   *TokenMap // nil: no response scanning (scanner not wired)
	metrics  *BrokerMetrics
	audit    AuditLogger // nil: audit disabled (never in production)
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
		identity: identity,
		policy:   policy,
		reader:   upstreamtoken.NewStrictTokenReader(tokenReader),
	}, nil
}

// WithScanCorrelation attaches the D6c response-scan correlation map: every
// successful injection records (request-id → SHA-256(header value) + scan
// needles) so the ext_proc scanner can detect the credential echoed back.
// tokens must come from the same process's scanner (in-memory only).
func (i *CredentialInjector) WithScanCorrelation(tokens *TokenMap) *CredentialInjector {
	i.tokens = tokens
	return i
}

// WithObservability attaches metrics and audit sinks. Both may be nil (tests);
// production wires both (cmd/thv-egressbroker).
func (i *CredentialInjector) WithObservability(metrics *BrokerMetrics, audit AuditLogger, podName string) *CredentialInjector {
	i.metrics = metrics
	i.audit = audit
	i.podName = podName
	return i
}

// Evaluate decides whether dest may carry the pod-owner's credential and, if
// so, returns the Authorization header mutation. It never logs credential
// material; deny reasons carry no token data. On allow, the scan-correlation
// record (keyed by requestID, the Envoy x-request-id) is written BEFORE the
// caller emits the header mutation, and the audit/metric events fire.
func (i *CredentialInjector) Evaluate(ctx context.Context, dest Destination, requestID string) Result {
	// D5 step 1: provider lookup — before any credential load.
	provider, ok := i.policy.ProviderFor(dest.Host)
	if !ok {
		return i.deny(ctx, dest, "", DenyReasonNoPolicy, "no provider allowlists this destination")
	}
	// D5 step 2: method + path check — still before any credential load.
	if !i.policy.AllowsMethod(provider, dest.Method) {
		return i.deny(ctx, dest, provider, DenyReasonMethodNotAllowed, "method outside provider policy")
	}
	if !i.policy.AllowsPath(provider, dest.Path) {
		return i.deny(ctx, dest, provider, DenyReasonPathNotAllowed, "path outside provider policy")
	}

	// Step 3: credential load, bound to the pod's own identity. The expected
	// binding's UserID is the pod-owner's raw sub from the downward-API
	// registry; Strict (forced by NewStrictTokenReader) fails closed on rows
	// that cannot prove their owner.
	creds, failed, err := i.reader.GetAllUpstreamCredentials(ctx, i.identity.SessionID,
		&storage.ExpectedBinding{UserID: i.identity.Subject, Strict: true})
	if err != nil {
		// Fail closed on storage errors (Redis down, binding violations):
		// never passthrough. A binding violation is reported by the store as an
		// error; distinguish it for the audit reason vocabulary.
		reason := DenyReasonStoreError
		if errors.Is(err, storage.ErrInvalidBinding) {
			reason = DenyReasonBindingMismatch
		}
		slog.DebugContext(ctx, "egressbroker: credential load failed; denying",
			"provider", provider, "error", err)
		return i.deny(ctx, dest, provider, reason, "upstream credential load failed")
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
		return i.deny(ctx, dest, provider, DenyReasonCredentialUnavailable,
			"upstream credential unavailable; re-consent required")
	}
	if cred.AccessToken == "" {
		// Defensive: a successful load with an empty token must never produce
		// an "Authorization: Bearer " header.
		return i.deny(ctx, dest, provider, DenyReasonCredentialUnavailable,
			"upstream credential unavailable; re-consent required")
	}

	// Step 4: the header value is in scope. Record the D6c scan correlation
	// BEFORE returning (the caller emits the mutation after this returns) —
	// the response can race ahead of any later hook.
	headerValue := "Bearer " + cred.AccessToken
	if i.tokens != nil {
		i.tokens.Record(requestID, headerValue, provider)
	}
	if i.audit != nil {
		i.audit.Inject(ctx, AuditEvent(i.identity, i.podName, dest, provider))
	}
	i.metrics.RecordInjection(ctx, i.identity.MCPServer, provider)

	return Result{
		Allow:       true,
		Provider:    provider,
		HeaderName:  AuthorizationHeader,
		HeaderValue: headerValue,
	}
}

// deny fires the deny audit + metric and returns the negative verdict.
func (i *CredentialInjector) deny(
	ctx context.Context, dest Destination, provider string, reason DenyReason, detail string,
) Result {
	validateDenyReason(reason)
	if i.audit != nil {
		i.audit.Deny(ctx, DenyEvent{
			InjectEvent: AuditEvent(i.identity, i.podName, dest, provider),
			Reason:      reason,
		})
	}
	i.metrics.RecordDenial(ctx, i.identity.MCPServer, provider, reason)
	return Result{Allow: false, Reason: reason, Provider: provider, DenyDetail: detail}
}
