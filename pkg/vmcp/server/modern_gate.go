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
//   - Every entry's comment must cite the issue tracking its Modern parity, so
//     a stale entry (parity shipped, entry forgotten) surfaces in issue triage
//     rather than silently keeping the instance off Modern.
//
//   - When Modern parity lands for a feature, delete its entry. The gate's
//     behavior is pinned by TestModernDispatchBlockers and
//     TestClassifyingHandler_ModernCapabilityGate (classification_test.go) plus
//     the full-handler pair in modern_gate_integration_test.go; deleting an
//     entry must flip cases there, so parity work cannot silently ship without
//     updating them.
//
//   - Loud refusals are deliberately out of scope. A composite workflow with an
//     elicitation step (config.WorkflowStepConfig type "elicitation") works over
//     Legacy sessions and fails Modern clients with an explicit -32021
//     MissingRequiredClientCapabilityError (or -32603 when the client declared
//     the capability) — see writeModernCallFailure. That is an honest error the
//     client can act on, not silently different behavior, so it does not gate
//     the whole instance off Modern for one workflow definition. Distinct from
//     #6059, which covers a Modern BACKEND returning input_required; here vMCP
//     itself is the elicitor.
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
	// session-scoped one; that work is tracked in #6089, and deleting this entry
	// is its definition of done. Until it lands, an optimizer-enabled instance
	// is Legacy-only. A non-nil s.optimizerFactory is a faithful "optimizer
	// enabled" signal: sessionmanager.New's constructor guard rejects an
	// optimizer without AdvertiseFromCore at startup, so the factory can never
	// be enabled yet invisible here.
	if s.optimizerFactory != nil {
		blocked = append(blocked, blockerOptimizer)
	}

	return blocked
}

// blockerOptimizer names the optimizer's entry in the blocker list;
// TestModernDispatchBlockers asserts on it by name.
const blockerOptimizer = "optimizer"
