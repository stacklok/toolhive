// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import "encoding/json"

// InputRequiredResult carries a Modern (2026-07-28) backend's
// resultType:"input_required" envelope — a Multi Round-Trip Request round
// (SEP-2322): the backend cannot complete the call until the caller fulfills
// InputRequests and retries the original request echoing RequestState.
//
// This is the domain-typed egress surface of MRTR (the seam the client-edge
// limitation in docs/arch/10-virtual-mcp-architecture.md names): the backend
// client decodes the envelope into this type and surfaces it through a typed
// error, so upper layers can relay a round to a Modern downstream client or
// fulfill it in-request for a Legacy one. Full flow design in
// docs/arch/16-vmcp-mrtr.md.
type InputRequiredResult struct {
	// InputRequests maps the backend's server-assigned keys to raw request
	// objects (ElicitRequest, CreateMessageRequest, or ListRootsRequest —
	// schema/draft's InputRequest union). Values are deliberately opaque
	// json.RawMessage: on the pass-through path vMCP relays them verbatim and
	// must not reinterpret them. Use Methods to inspect only the wire method
	// names (e.g. for capability gating).
	InputRequests map[string]json.RawMessage

	// RequestState is the backend's opaque round-trip state. Per SEP-2322 the
	// client "MUST echo back the exact value" on retry and "MUST NOT inspect,
	// parse, modify, or make any assumptions about" it — vMCP relays it
	// verbatim and never interprets it. Empty when the backend sent none.
	RequestState string
}

// Methods returns the JSON-RPC method name of each input request, keyed by
// the backend's request key. An entry whose value does not decode as an
// object with a "method" field maps to the empty string, so callers gating on
// method (capability checks) fail closed on malformed entries instead of
// skipping them.
func (r *InputRequiredResult) Methods() map[string]string {
	if len(r.InputRequests) == 0 {
		return nil
	}
	methods := make(map[string]string, len(r.InputRequests))
	for key, raw := range r.InputRequests {
		var probe struct {
			Method string `json:"method"`
		}
		// A failed decode leaves probe.Method empty — the fail-closed value.
		_ = json.Unmarshal(raw, &probe)
		methods[key] = probe.Method
	}
	return methods
}
