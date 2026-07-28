// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

// modernDispatchBlockers enumerates the enabled features of THIS instance that
// the stateless Modern (2026-07-28) dispatch path cannot serve yet. It is the
// capability gate that decides whether vMCP advertises and serves the Modern
// revision: an empty result means every enabled feature is servable by
// dispatchModern and Modern requests are dispatched; a non-empty result means
// classifyingHandler keeps Modern-capable clients on Legacy (see the gate
// branch there for the wire mechanics).
//
// Contract for this list:
//
//   - One entry per feature, each guarded by the narrowest signal that the
//     feature is actually enabled on this instance, with a comment saying WHY
//     the Modern path cannot serve it. "Cannot serve" means a Modern client
//     would silently receive different behavior than the feature promises —
//     not merely that the feature is session-flavored. A feature that Modern
//     clients simply don't need (e.g. Redis-backed session sharing: Legacy
//     clients keep their shared sessions, Modern clients are sessionless by
//     design and store nothing) does NOT belong here; coexistence of that kind
//     is asserted by test/e2e/thv-operator/virtualmcp/virtualmcp_dual_era_redis_test.go.
//
//   - When Modern parity lands for a feature, delete its entry. The gate's
//     behavior is pinned by TestModernDispatchBlockers and
//     TestClassifyingHandler_ModernCapabilityGate (classification_test.go) plus
//     the full-handler pair in modern_gate_integration_test.go; deleting an
//     entry must flip cases there, so parity work cannot silently ship without
//     updating them.
//
// The result is derived from construction-time configuration only, so it is
// constant for the life of the Server; Serve logs it once at startup.
func (s *Server) modernDispatchBlockers() []string {
	var blocked []string

	// Optimizer: find_tool/call_tool are Serve-layer, session-scoped meta-tools
	// (serve_optimizer.go). Each session builds an FTS5 index over its advertised
	// set and swaps the two meta-tools in place of the raw tools; the index is
	// transport/session state and is deliberately NOT in the stateless core.
	// dispatchModern serves tools/list and tools/call straight from
	// core.ListTools/core.CallTool, so a Modern client of an optimizer-enabled
	// instance would silently receive the full raw aggregated tool set and
	// `tools/call find_tool` would fail -32603 "not found" — the optimizer
	// feature would be invisibly disabled for exactly the newest clients.
	// Modern parity needs an identity- or instance-scoped index to replace the
	// session-scoped one; until that lands, an optimizer-enabled instance is
	// Legacy-only. s.optimizerFactory is the resolved factory and is non-nil on
	// both composition paths (New and direct Serve) exactly when the optimizer
	// is enabled.
	if s.optimizerFactory != nil {
		blocked = append(blocked, "optimizer")
	}

	return blocked
}
