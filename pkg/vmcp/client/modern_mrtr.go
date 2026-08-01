// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// mrtrMethods are the only client requests that may carry an input_required
// round: "Servers MUST NOT send InputRequiredResult responses on any other
// client requests" (spec, basic/patterns/mrtr). The set coincides with
// pkg/mcp's nameRequiredMethods today, but the concepts are independent —
// Mcp-Name validation is a header rule, this is the MRTR method allow-list,
// and the final 2026-07-28 cut may grow this one alone (SEP-2322 Final also
// allowed GetTaskPayloadRequest before tasks moved to an extension; see the
// drift list in docs/arch/16-vmcp-mrtr.md) — so they are deliberately not
// shared.
var mrtrMethods = map[string]bool{
	"tools/call":     true,
	"resources/read": true,
	"prompts/get":    true,
}

// newInputRequiredError builds the typed error (vmcp.InputRequiredError,
// unwrapping to errModernInputRequired) for a non-"complete" Modern result
// envelope. The error always classifies and renders identically to the
// previous fmt.Errorf wrapping; whether it also carries an extractable round
// (vmcp.InputRequiredFromError reporting ok=true) is gated fail-closed:
//
//   - resultType must be exactly "input_required" — the spec says an
//     unrecognized resultType "MUST be considered invalid", so anything else
//     (including the Tasks extension's "task", which vMCP never solicits —
//     it advertises no extensions) keeps pure sentinel semantics;
//   - method must be one of mrtrMethods — a round on any other request is a
//     backend protocol violation, and relaying it would make vMCP the
//     non-conformant server;
//   - the payload must decode strictly and be drivable: server requirement 6
//     mandates at least one of inputRequests or requestState, and an envelope
//     violating it (or one with a wrong-typed field) must not surface as a
//     round — a consumer following client requirement 1 would retry it having
//     gathered nothing, which the backend may answer with the same envelope
//     again, a loop with no exit. Fail closed to the sentinel instead.
func newInputRequiredError(method, resultType string, result json.RawMessage) error {
	e := &vmcp.InputRequiredError{ResultType: resultType, Sentinel: errModernInputRequired}
	if resultType != modernResultTypeInputRequired || !mrtrMethods[method] {
		return e
	}
	var payload struct {
		InputRequests map[string]json.RawMessage `json:"inputRequests"`
		RequestState  *string                    `json:"requestState"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		// Wrong-typed inputRequests/requestState: a schema violation, not a
		// drivable round. The sentinel classification survives.
		return e
	}
	if len(payload.InputRequests) == 0 && payload.RequestState == nil {
		// Server requirement 6 violation ("at least one of inputRequests or
		// requestState"): nothing to fulfill and nothing to echo.
		return e
	}
	e.Result = &vmcp.InputRequiredResult{
		InputRequests: payload.InputRequests,
		RequestState:  payload.RequestState,
	}
	return e
}
