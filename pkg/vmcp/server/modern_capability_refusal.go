// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
)

// Client capability names as they appear in the draft schema's
// ClientCapabilities object — the keys a Modern client declares under
// _meta's io.modelcontextprotocol/clientCapabilities and the keys a
// MissingRequiredClientCapabilityError reports back in
// data.requiredCapabilities.
const (
	capabilityElicitation = "elicitation"
	capabilitySampling    = "sampling"
)

// modernClientCapabilitiesKey is pkg/mcp's metaKeyClientCapabilities,
// reproduced by hand because that constant is unexported — the same precedent
// as modernServerInfoKey in modern_envelope.go. Keep the two in sync.
const modernClientCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"

// modernClientDeclaredCapability reports whether the request's _meta declared
// the named client capability. Presence of the key is the declaration (its
// value is a per-capability options object, typically {}), matching how
// ClassifyRevision treats clientCapabilities itself: presence, not content.
func modernClientDeclaredCapability(meta map[string]any, capability string) bool {
	caps, _ := meta[modernClientCapabilitiesKey].(map[string]any)
	_, ok := caps[capability]
	return ok
}

// capabilityRefusalRecorder captures, in-process, which client capability a
// backend's mid-call server-initiated request needed when there was no
// downstream session to forward it to (Modern ingress). dispatchModern's call
// verbs install one before dispatching; the SDK requester adapters
// (sdk_elicitation_adapter.go, sdk_sampling_adapter.go) record into it at the
// exact point of refusal (mcpcompat's ErrNoActiveSession); the dispatcher
// reads it after a failed call to classify the failure as the draft schema's
// MissingRequiredClientCapabilityError (-32021) when the client did not
// declare the capability.
//
// Why this rides the context rather than a parameter or the error chain: the
// refusal evidence cannot travel back through the return path. The forwarder
// answers the BACKEND's elicitation/sampling request; the backend then fails
// its own tool, and what the dispatcher receives is the backend's error
// STRING — the typed sentinel is laundered away at the wire boundary. A
// Modern request has no MultiSession to hang per-call state on (that absence
// is the very thing being recorded), and the requesters are bound once at
// startup (BindForwarders), so a per-call explicit parameter cannot reach
// them either. This mirrors the audit.BackendInfoFromContext pattern the same
// dispatch verbs already use: a transport-boundary observation channel
// written by a lower layer, not domain data flowing between middleware
// (vmcp-anti-patterns #1 targets the latter).
type capabilityRefusalRecorder struct {
	mu         sync.Mutex
	capability string
}

// capabilityRefusalKey is the context key for the recorder.
type capabilityRefusalKey struct{}

// withCapabilityRefusalRecorder returns ctx carrying a fresh recorder, plus
// the recorder itself for the caller to read after dispatch. Installed only
// by dispatchModern's call verbs; on the Legacy/SDK path no recorder exists
// and recordCapabilityRefusal is a benign no-op (Legacy calls have a session,
// so the refusal it observes cannot occur there in the first place).
func withCapabilityRefusalRecorder(ctx context.Context) (context.Context, *capabilityRefusalRecorder) {
	rec := &capabilityRefusalRecorder{}
	return context.WithValue(ctx, capabilityRefusalKey{}, rec), rec
}

// recordCapabilityRefusal notes that a mid-call server-initiated request
// needing the named capability was refused for lack of a downstream session.
// The first recorded capability wins — one call verb produces one wire error,
// and the first refusal is what failed the backend tool. No-op when ctx
// carries no recorder.
func recordCapabilityRefusal(ctx context.Context, capability string) {
	rec, _ := ctx.Value(capabilityRefusalKey{}).(*capabilityRefusalRecorder)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.capability == "" {
		rec.capability = capability
	}
}

// refused returns the recorded capability name, or "" if no refusal occurred.
func (r *capabilityRefusalRecorder) refused() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.capability
}
