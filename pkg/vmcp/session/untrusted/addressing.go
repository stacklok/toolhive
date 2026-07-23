// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// MetadataKeyUntrusted marks a discovered backend as untrusted (set by the
// k8s workload discoverer from MCPServer.spec.untrusted).
const MetadataKeyUntrusted = "toolhive.stacklok.dev/untrusted"

// MetadataKeyMCPServerUID carries the MCPServer CR UID on a discovered
// backend (rename-safe identity; set by the k8s workload discoverer).
const MetadataKeyMCPServerUID = "toolhive.stacklok.dev/mcpserver-uid"

// MetadataKeyUntrustedPodName records which pod serves this session's
// backend. Observability only; routing uses BaseURL.
const MetadataKeyUntrustedPodName = "toolhive.stacklok.dev/untrusted-pod"

// MetadataKeyUntrustedPodUID records the pod UID for observability.
const MetadataKeyUntrustedPodUID = "toolhive.stacklok.dev/untrusted-pod-uid"

// BackendAddressResolver rewrites per-session backend targets for untrusted
// backends so they point at the session's single-tenant pod.
type BackendAddressResolver interface {
	// ResolveTargets returns backends for this session with untrusted backends'
	// BaseURL rewritten to their per-session pod. May create pods. Soft-fails
	// individual backends (absent from the result), matching the partial-init
	// semantics of the session factory. Trusted backends pass through
	// unmodified. Registry backends are never mutated.
	//
	// onNewPod, when non-nil, is invoked with the backend ID each time
	// provisioning CREATES a fresh pod (not when an existing pod is adopted):
	// the caller drops the backend's stale Mcp-Session-Id hint so a session ID
	// from the deleted pod's previous incarnation is never sent to the new pod.
	ResolveTargets(
		ctx context.Context, sess SessionRef, backends []*vmcp.Backend, onNewPod func(backendID string),
	) []*vmcp.Backend
}

// podAddressResolver is the production BackendAddressResolver.
type podAddressResolver struct {
	lifecycle   PodLifecycle
	admission   Admission
	readyBudget time.Duration
	tokenStore  *TokenStoreConfig
	images      *SidecarImages
	sidecarRes  *SidecarResourceOverride
	metrics     *untrustedMetrics
}

// NewPodAddressResolver creates the resolver. readyBudget bounds the per-pod
// cold-start wait (DefaultReadyBudget when zero).
func NewPodAddressResolver(lifecycle PodLifecycle, adm Admission, readyBudget time.Duration) BackendAddressResolver {
	if readyBudget <= 0 {
		readyBudget = DefaultReadyBudget
	}
	return &podAddressResolver{lifecycle: lifecycle, admission: adm, readyBudget: readyBudget}
}

// podAddressResolverWithTokenStore is the internal constructor that also wires
// the egress-broker token-store coordinates, sidecar images/resources, and
// metrics into every provisioned pod. Kept unexported: production wiring goes
// through NewStack, which resolves them once; NewPodAddressResolver stays the
// public seam for tests that exercise the resolver without them.
func podAddressResolverWithTokenStore(
	lifecycle PodLifecycle, adm Admission, readyBudget time.Duration, ts *TokenStoreConfig, m *untrustedMetrics,
	images *SidecarImages, sidecarRes *SidecarResourceOverride,
) *podAddressResolver {
	r := NewPodAddressResolver(lifecycle, adm, readyBudget).(*podAddressResolver)
	r.tokenStore = ts
	r.images = images
	r.sidecarRes = sidecarRes
	r.metrics = m
	return r
}

// ResolveTargets implements BackendAddressResolver. Anonymous/unauthenticated
// sessions (empty issuer or subject) soft-fail every untrusted backend: there
// is no user to bind a pod to.
func (r *podAddressResolver) ResolveTargets(
	ctx context.Context, sess SessionRef, backends []*vmcp.Backend, onNewPod func(backendID string),
) []*vmcp.Backend {
	out := make([]*vmcp.Backend, 0, len(backends))
	authenticated := sess.Issuer != "" && sess.Subject != ""

	for _, b := range backends {
		if b == nil {
			// Defensive: the factory filters nils before invoking the resolver,
			// but a nil must never be passed through to the init fan-out.
			continue
		}
		if b.Metadata[MetadataKeyUntrusted] != "true" {
			out = append(out, b)
			continue
		}
		if !authenticated {
			slog.Warn("untrusted backend excluded: session is anonymous; there is no user to bind a pod to",
				"backend", b.Name)
			continue
		}
		resolved, err := r.resolveOne(ctx, sess, b, onNewPod)
		if err != nil {
			logSoftFailure(b, err)
			continue
		}
		out = append(out, resolved)
	}
	return out
}

// resolveOne provisions the pod for one untrusted backend and returns a copy
// of the backend with the per-session BaseURL. The input is never mutated.
// onNewPod (may be nil) fires with the backend ID only when EnsurePod created
// the pod — the caller drops the stale session hint for that backend.
func (r *podAddressResolver) resolveOne(
	ctx context.Context, sess SessionRef, b *vmcp.Backend, onNewPod func(backendID string),
) (*vmcp.Backend, error) {
	userKey := sess.UserKey()
	mcpserverUID := b.Metadata[MetadataKeyMCPServerUID]
	if mcpserverUID == "" {
		return nil, fmt.Errorf("untrusted backend %q has no MCPServer UID metadata", b.Name)
	}

	if r.admission != nil {
		if err := r.admission.Check(ctx, userKey, mcpserverUID); err != nil {
			r.metrics.recordAdmission(ctx, admissionResult(err))
			return nil, err
		}
		r.metrics.recordAdmission(ctx, "admitted")
	}

	port, suffix, err := BackendPortAndSuffix(b.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("untrusted backend %q has unparsable BaseURL: %w", b.Name, err)
	}

	pod, err := r.lifecycle.EnsurePod(ctx, EnsurePodRequest{
		Session:          sess,
		MCPServerUID:     mcpserverUID,
		MCPServerName:    b.Name,
		Port:             port,
		OwnerRefUID:      types.UID(mcpserverUID),
		TokenStore:       r.tokenStore,
		Images:           r.images,
		SidecarResources: r.sidecarRes,
		OnNewPod: func(string) {
			if onNewPod != nil {
				onNewPod(b.ID)
			}
		},
	})
	if err != nil {
		return nil, err
	}

	if err := r.lifecycle.WaitReady(ctx, pod, r.readyBudget); err != nil {
		return nil, err
	}

	// Copy before mutating: the registry backend is shared across sessions.
	b2 := *b
	b2.Metadata = maps.Clone(b.Metadata)
	if b2.Metadata == nil {
		b2.Metadata = make(map[string]string)
	}
	b2.BaseURL = fmt.Sprintf("http://%s:%d%s", pod.Status.PodIP, port, suffix)
	b2.Metadata[MetadataKeyUntrustedPodName] = pod.Name
	b2.Metadata[MetadataKeyUntrustedPodUID] = string(pod.UID)
	return &b2, nil
}

// admissionResult maps an admission error to the low-cardinality result label
// for untrusted_pod_admissions_total. Deliberately a fixed vocabulary — never
// a user, pod, or server identifier (ADR D11).
func admissionResult(err error) string {
	if errors.Is(err, ErrQuotaExceeded) {
		return "quota_exceeded"
	}
	return "error"
}

// logSoftFailure logs a per-backend soft failure at WARN. The reason string is
// deliberately coarse (no user identifiers beyond the backend name).
func logSoftFailure(b *vmcp.Backend, err error) {
	reason := "ensure_pod_failed"
	if errors.Is(err, ErrQuotaExceeded) {
		reason = "quota_exceeded"
	}
	slog.Warn("untrusted backend excluded from session",
		"backend", b.Name,
		"reason", reason,
		"error", err,
	)
}

// BackendPortAndSuffix extracts the MCP port and URL path/fragment suffix from
// the discovered shared BaseURL (built by transport.GenerateMCPServerURL —
// SSE/stdio targets carry a "#<containerName>" fragment) so the pod-IP rewrite
// preserves transport semantics, e.g. "http://svc:8080#name" →
// "http://<podIP>:8080#name".
func BackendPortAndSuffix(baseURL string) (port int32, suffix string, err error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return 0, "", fmt.Errorf("failed to parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return 0, "", fmt.Errorf("unsupported scheme %q in base URL", u.Scheme)
	}
	portStr := u.Port()
	if portStr == "" {
		return 0, "", fmt.Errorf("base URL %q has no explicit port", baseURL)
	}
	p, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil || p <= 0 {
		return 0, "", fmt.Errorf("invalid port %q in base URL", portStr)
	}

	suffix = u.EscapedPath()
	if u.RawQuery != "" {
		suffix += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		suffix += "#" + u.Fragment
	}
	// Reject URLs whose host:port would leak credentials or user-info into the
	// rewritten address.
	if u.User != nil || strings.Contains(u.Host, "@") {
		return 0, "", fmt.Errorf("base URL %q must not carry user info", baseURL)
	}
	return int32(p), suffix, nil
}
