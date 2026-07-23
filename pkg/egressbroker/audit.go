// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Audit events for the credential broker (ADR-0001 D11). The audit trail is
// the primary detection surface for authority-abuse-within-scope (ADR §9.5):
// every credential injection, every denial, and every suppressed response-side
// leak emits exactly one structured slog line to stdout → cluster log pipeline
// (same sink as every other component).
//
// Cardinality rule (D11): the user sub appears ONLY here — hashed
// (SHA-256[:16hex], correlation without raw PII in stdout) — and never in
// metric labels.
//
// NEVER logged: token values, request/response bodies, KEKs, CA material.

// DenyReason is the fixed vocabulary of injection-denial causes. Reasons must
// carry no token data and no request body content.
type DenyReason string

const (
	// DenyReasonNoPolicy means no provider allowlists the destination host.
	DenyReasonNoPolicy DenyReason = "no-policy"
	// DenyReasonMethodNotAllowed means the method is outside the provider's policy.
	DenyReasonMethodNotAllowed DenyReason = "method-not-allowed"
	// DenyReasonPathNotAllowed means the path is outside the provider's policy.
	DenyReasonPathNotAllowed DenyReason = "path-not-allowed"
	// DenyReasonCredentialUnavailable means no (valid) credential exists for
	// the provider; re-consent is required.
	DenyReasonCredentialUnavailable DenyReason = "credential-unavailable" //nolint:gosec // G101: a reason label, not a credential
	// DenyReasonBindingMismatch means the stored credential's owner binding
	// does not match the pod's identity (or the row cannot prove its owner in
	// Strict mode).
	DenyReasonBindingMismatch DenyReason = "binding-mismatch"
	// DenyReasonStoreError means the credential store itself failed.
	DenyReasonStoreError DenyReason = "store-error"
	// DenyReasonMalformed means the ext_authz request carried no parseable
	// destination (fail-closed parse refusal).
	DenyReasonMalformed DenyReason = "malformed-request"
)

// InjectEvent is emitted once per successful credential injection.
type InjectEvent struct {
	MCPServer string
	Pod       string
	// Subject is the raw OIDC sub; it is hashed before it ever reaches the
	// log line (see SubjectHash).
	Subject  string
	Provider string
	Host     string
	Method   string
	Path     string
}

// DenyEvent is emitted once per injection denial.
type DenyEvent struct {
	InjectEvent
	Reason DenyReason
}

// LeakEvent is emitted when the response scanner suppresses a response that
// echoed the injected credential back (D6c). RequestID is the Envoy
// x-request-id correlator.
type LeakEvent struct {
	MCPServer string
	Provider  string
	Host      string
	RequestID string
	Where     LeakLocation
}

// AuditLogger is the broker's audit sink. Implementations must be safe for
// concurrent use and must never emit credential material.
type AuditLogger interface {
	Inject(ctx context.Context, e InjectEvent)
	Deny(ctx context.Context, e DenyEvent)
	Leak(ctx context.Context, e LeakEvent)
}

// SubjectHash returns the correlation-safe form of a raw OIDC sub: the first
// 16 hex chars of its SHA-256. Raw subs never reach stdout.
func SubjectHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])[:16]
}

// slogAuditLogger emits one structured slog line per event to the given
// handler's sink (stdout in production).
type slogAuditLogger struct {
	logger *slog.Logger
}

// NewAuditLogger returns the stdout slog-backed audit sink (production).
func NewAuditLogger() AuditLogger {
	return NewAuditLoggerWith(slog.NewJSONHandler(os.Stdout, nil))
}

// NewAuditLoggerWith returns an audit sink on an arbitrary slog handler
// (tests capture with an in-memory handler).
func NewAuditLoggerWith(handler slog.Handler) AuditLogger {
	if handler == nil {
		// Fail loudly: a nil handler would silently drop the audit trail of a
		// credential broker.
		panic("egressbroker: audit slog handler must not be nil")
	}
	return &slogAuditLogger{logger: slog.New(handler)}
}

// Inject implements AuditLogger.
func (a *slogAuditLogger) Inject(ctx context.Context, e InjectEvent) {
	a.logger.LogAttrs(ctx, slog.LevelInfo, "egressbroker credential injected", auditAttrs(e)...)
}

// Deny implements AuditLogger.
func (a *slogAuditLogger) Deny(ctx context.Context, e DenyEvent) {
	attrs := append(auditAttrs(e.InjectEvent), slog.String("reason", string(e.Reason)))
	a.logger.LogAttrs(ctx, slog.LevelInfo, "egressbroker injection denied", attrs...)
}

// Leak implements AuditLogger.
func (a *slogAuditLogger) Leak(ctx context.Context, e LeakEvent) {
	a.logger.WarnContext(ctx, "egressbroker response suppressed: credential echo detected",
		slog.String("mcpserver", e.MCPServer),
		slog.String("provider", e.Provider),
		slog.String("host", e.Host),
		slog.String("request_id", e.RequestID),
		slog.String("where", string(e.Where)),
	)
}

// auditAttrs renders the shared Inject/Deny fields. The sub is hashed here —
// the only place the raw value is consumed.
func auditAttrs(e InjectEvent) []slog.Attr {
	return []slog.Attr{
		slog.Time("ts", time.Now()),
		slog.String("mcpserver", e.MCPServer),
		slog.String("pod", e.Pod),
		slog.String("sub_hash", SubjectHash(e.Subject)),
		slog.String("provider", e.Provider),
		slog.String("host", e.Host),
		slog.String("method", e.Method),
		slog.String("path", e.Path),
	}
}

// AuditEvent builds an InjectEvent for one decision from the pod identity and
// destination. podName is optional context (downward-API pod name; empty is
// tolerated — the event still carries the correlation keys).
func AuditEvent(identity PodIdentity, podName string, dest Destination, provider string) InjectEvent {
	return InjectEvent{
		MCPServer: identity.MCPServer,
		Pod:       podName,
		Subject:   identity.Subject,
		Provider:  provider,
		Host:      dest.Host,
		Method:    dest.Method,
		Path:      dest.Path,
	}
}

// validateDenyReason guards the enum at the Deny call sites (fail loudly on a
// reason outside the fixed vocabulary — a typo'd reason would break
// alerting dashboards keyed on it).
func validateDenyReason(r DenyReason) {
	switch r {
	case DenyReasonNoPolicy, DenyReasonMethodNotAllowed, DenyReasonPathNotAllowed,
		DenyReasonCredentialUnavailable, DenyReasonBindingMismatch, DenyReasonStoreError,
		DenyReasonMalformed:
	default:
		panic(fmt.Sprintf("egressbroker: unknown deny reason %q", r))
	}
}
