// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package untrusted implements the session-scoped single-tenant backend
// lifecycle for untrusted MCPServers behind a vMCP (ADR-0001 D2/D4).
//
// For each (user, session, untrusted MCPServer) tuple the vMCP data plane
// provisions one bare Pod cloned from the operator-built backend StatefulSet's
// pod template, rewrites the per-session backend target BaseURL to the pod IP,
// and reaps pods whose session has gone idle (in-process reaper goroutine).
//
// Security invariants enforced here:
//   - Pods are only ever created by cloning the live backend StatefulSet
//     template (never from caller-supplied spec), with a fail-closed
//     re-verification that no Secret/ConfigMap-sourced env or volumes reach
//     the backend container.
//   - Raw user identifiers never appear in labels, annotations, or logs
//     beyond a documented sha256 prefix (PII).
package untrusted

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/stacklok/toolhive/pkg/egressbroker"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
)

const (
	// LabelUntrusted marks a pod as an untrusted per-session backend pod.
	LabelUntrusted = "toolhive.stacklok.dev/untrusted"

	// LabelUntrustedUser carries sha256(UserKey)[:40] — never the raw sub (PII).
	LabelUntrustedUser = "toolhive.stacklok.dev/untrusted-user"

	// LabelUntrustedSession carries a short hash of the session ID.
	LabelUntrustedSession = "toolhive.stacklok.dev/untrusted-session"

	// LabelMCPServerUID carries the MCPServer CR UID (rename-safe).
	LabelMCPServerUID = "toolhive.stacklok.dev/mcpserver-uid"

	// LabelApp is inherited from the backend StatefulSet so per-session pods
	// match the same NetworkPolicy selectors as the shared backend pods.
	LabelApp = "app"

	// AnnotationIssuer carries the raw OIDC issuer for the Wave-3 sidecar's
	// downward-API registry (public, non-sensitive). Re-exported from
	// pkg/egressbroker — the shared owner of the pod-annotation ↔ broker-env
	// contract (this package is the writer, the broker the reader).
	AnnotationIssuer = egressbroker.AnnotationIssuer

	// AnnotationSubjectHash carries sha256(sub)[:40] — never the raw sub.
	AnnotationSubjectHash = "toolhive.stacklok.dev/sub"

	// AnnotationSubjectRaw carries the raw OIDC subject (see
	// egressbroker.AnnotationSubjectRaw for the full rationale).
	AnnotationSubjectRaw = egressbroker.AnnotationSubjectRaw

	// AnnotationMCPServerName carries the MCPServer CR name.
	AnnotationMCPServerName = egressbroker.AnnotationMCPServerName

	// AnnotationSessionID carries the vMCP session ID this pod serves.
	// Checked on pod reuse: a mismatch means a hash collision or a
	// cross-session pod — fail closed, never attach.
	AnnotationSessionID = egressbroker.AnnotationSessionID

	// AnnotationVMCPUid identifies the vMCP instance that provisioned the pod
	// (heartbeat identity for the reaper's zombie rule).
	AnnotationVMCPUid = "toolhive.stacklok.dev/vmcp-uid"

	// AnnotationCAGeneration is stamped by the OPERATOR on the backend
	// StatefulSet's pod template: the bump-CA generation (cert hash) session
	// pods must mount. The lifecycle reads it when cloning so a mid-rotation
	// clone gets one consistent cert/key pair. The operator keeps its own
	// mirror constant (module boundary); the contract is pinned by tests.
	AnnotationCAGeneration = "toolhive.stacklok.dev/bump-ca-generation"
)

// PodNameFor returns the deterministic pod name for the (mcpserverUID,
// userKey, sessionID) tuple: "untrusted-<uid[:13]>-<sha256(userKey|sessionID)[:24]>".
// The components bound the name to at most 10+13+1+24 = 48 characters, always
// within the 63-char DNS-1123 label limit (the trailing truncate is a defensive
// no-op). Deterministic naming makes EnsurePod an idempotent get-then-create
// (the name is the idempotency key) and keeps cross-vMCP-pod session restore
// collision-free.
func PodNameFor(mcpserverUID, userKey, sessionID string) string {
	uid := mcpserverUID
	if len(uid) > 13 {
		uid = uid[:13]
	}
	sum := sha256.Sum256([]byte(userKey + "|" + sessionID))
	name := fmt.Sprintf("untrusted-%s-%s", uid, hex.EncodeToString(sum[:])[:24])
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// userHash is the canonical hashed user identity used in labels, annotations,
// and Redis keys: sha256(UserKey)[:40].
func userHash(userKey string) string {
	sum := sha256.Sum256([]byte(userKey))
	return hex.EncodeToString(sum[:])[:40]
}

// subjectHash hashes only the OIDC subject for the sub annotation (Wave-3
// downward-API registry); the issuer is public and stored raw.
func subjectHash(sub string) string {
	sum := sha256.Sum256([]byte(sub))
	return hex.EncodeToString(sum[:])[:40]
}

// sessionHash is the short session-ID hash used in the untrusted-session label.
func sessionHash(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:40]
}

// SessionRef is the identity tuple a pod is provisioned for.
type SessionRef struct {
	SessionID string
	Issuer    string
	Subject   string
	Namespace string // vMCP's own namespace (via k8s.GetCurrentNamespace)
	// CAGeneration is the bump-CA generation (from the backend StatefulSet
	// template's AnnotationCAGeneration) the cloned pod must mount. The
	// lifecycle sets it while resolving the clone template; required by the
	// egress sidecar wiring (fail closed when absent).
	CAGeneration string
}

// UserKey is the canonical registry identity. It uses binding.Format (NUL-joined
// iss,sub) so the registry key and the on-the-wire session binding are the same
// canonical form. Callers must only construct SessionRef with non-empty
// Issuer/Subject; an empty half yields the empty string, which untrusted
// sessions must never carry (anonymous sessions soft-fail untrusted backends).
func (s SessionRef) UserKey() string {
	b, err := binding.Format(s.Issuer, s.Subject)
	if err != nil {
		return ""
	}
	return b
}
