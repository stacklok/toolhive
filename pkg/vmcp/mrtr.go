// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// InputRequiredResult carries a Modern (2026-07-28) backend's
// resultType:"input_required" envelope — a Multi Round-Trip Request round
// (SEP-2322): the backend cannot complete the call until the caller fulfills
// InputRequests and retries the original request echoing RequestState.
//
// This is the domain-typed egress surface of MRTR (the seam the client-edge
// limitation in docs/arch/10-virtual-mcp-architecture.md names): the backend
// client decodes the envelope into this type and surfaces it through
// InputRequiredError, so upper layers can relay a round to a Modern
// downstream client or fulfill it in-request for a Legacy one. Full flow
// design in docs/arch/16-vmcp-mrtr.md.
type InputRequiredResult struct {
	// InputRequests maps the backend's server-assigned keys to raw request
	// objects (ElicitRequest, CreateMessageRequest, or ListRootsRequest —
	// schema/draft's InputRequest union). Values are deliberately opaque
	// json.RawMessage: on the pass-through path vMCP relays them verbatim and
	// must not reinterpret them. Use Methods to inspect only the wire method
	// names (e.g. for capability gating). Nil on a load-shedding round
	// (RequestState only).
	InputRequests map[string]json.RawMessage

	// RequestState is the backend's opaque round-trip state. Per SEP-2322 the
	// client "MUST echo back the exact value" on retry — vMCP relays it
	// verbatim and never interprets it. Nil when the backend's envelope had no
	// requestState field, which the retry must preserve: client requirement 2
	// says that if the result does not contain the field, the client "MUST NOT
	// include one in the retry". The schema puts no minimum length on the
	// field, so present-and-empty ("") is legal, distinct from absent, and
	// must be echoed as the empty string.
	RequestState *string
}

// InputRequiredError is the typed error that carries an input_required round
// through the backend client's error return, so every existing
// complete-result path stays untouched. Result is non-nil only for a valid,
// drivable round (see the extraction rules on InputRequiredFromError); for
// any other non-"complete" envelope only the sentinel classification remains.
//
// Sentinel is the classification error this unwraps to — the backend client
// sets its errModernInputRequired so every existing errors.Is check keeps
// working unchanged. The fields are exported so tests (e.g. against
// mocks.MockBackendClient) can construct the error without going through the
// real client's envelope decode.
type InputRequiredError struct {
	// ResultType is the envelope's raw resultType. Backend-controlled text:
	// Error() truncates it, and it must never be interpolated elsewhere
	// unbounded.
	ResultType string

	// Result is the decoded round, nil unless the envelope was a valid
	// input_required round on a method that may carry one.
	Result *InputRequiredResult

	// Sentinel is the error Unwrap returns; required. It leads the Error()
	// message, so the rendered text stays byte-identical to a plain
	// fmt.Errorf("%w: ...", Sentinel, ...) wrapping.
	Sentinel error
}

// maxResultTypeInError bounds how much of the backend-controlled resultType
// Error() interpolates. Real resultTypes are short tokens ("complete",
// "input_required", "task"); anything longer is hostile or broken, and this
// error's text reaches the downstream client via writeModernDispatchError —
// report a prefix and the length instead of splicing the payload (the #6079 /
// #6066 rule).
const maxResultTypeInError = 64

func (e *InputRequiredError) Error() string {
	if len(e.ResultType) > maxResultTypeInError {
		return fmt.Sprintf("%s: resultType=%q... (%d bytes)",
			e.Sentinel, e.ResultType[:maxResultTypeInError], len(e.ResultType))
	}
	return fmt.Sprintf("%s: resultType=%q", e.Sentinel, e.ResultType)
}

// Unwrap returns the classification sentinel, keeping every existing
// errors.Is check on the untyped sentinel working unchanged.
func (e *InputRequiredError) Unwrap() error { return e.Sentinel }

// InputRequiredFromError extracts the SEP-2322 input_required payload from an
// error chain, however deeply the backend client wrapped it. It reports false
// for errors that are not a drivable input_required round — including the
// unrecognized-resultType, disallowed-method, and invalid-payload variants of
// the same sentinel — so callers can use it as the single MRTR branch point:
//
//	if round, ok := vmcp.InputRequiredFromError(err); ok { ... relay round ... }
//
// This is the seam the ingress half (dispatchModern's input_required envelope
// and the Legacy-client bridge; docs/arch/16-vmcp-mrtr.md slices 2-4) consumes.
func InputRequiredFromError(err error) (*InputRequiredResult, bool) {
	var ire *InputRequiredError
	if errors.As(err, &ire) && ire.Result != nil {
		return ire.Result, true
	}
	return nil, false
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
