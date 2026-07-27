// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// inputRequiredError is the typed form of errModernInputRequired: a Modern
// envelope decoded with a non-"complete" resultType. For
// resultType:"input_required" it carries the decoded SEP-2322 payload so
// upper layers can drive a Multi Round-Trip Request round (relay to a Modern
// downstream client, or fulfill in-request for a Legacy one — see
// docs/arch/16-vmcp-mrtr.md); for any other unrecognized resultType the
// payload is nil and only the sentinel classification remains.
//
// It unwraps to errModernInputRequired so every existing errors.Is check —
// notably probeRevision's Modern-positive classification (client.go) — keeps
// working unchanged, and Error() renders byte-identically to the previous
// fmt.Errorf wrapping so no client-visible message shifts.
type inputRequiredError struct {
	resultType string
	result     *vmcp.InputRequiredResult
}

func (e *inputRequiredError) Error() string {
	return fmt.Sprintf("%s: resultType=%q", errModernInputRequired, e.resultType)
}

func (*inputRequiredError) Unwrap() error { return errModernInputRequired }

// newInputRequiredError builds the typed error for a non-"complete" Modern
// result envelope. Only resultType:"input_required" carries a payload: the
// spec says clients "SHOULD treat unrecognized values as invalid protocol
// responses", so other resultTypes keep pure sentinel semantics. The payload
// decode is tolerant — a malformed inputRequests/requestState still yields
// the typed error with whatever decoded (the round is undrivable either way,
// and the caller's error handling must not depend on backend well-formedness).
func newInputRequiredError(resultType string, result json.RawMessage) error {
	e := &inputRequiredError{resultType: resultType}
	if resultType != "input_required" {
		return e
	}
	var payload struct {
		InputRequests map[string]json.RawMessage `json:"inputRequests"`
		RequestState  string                     `json:"requestState"`
	}
	// Tolerant decode: see the doc comment.
	_ = json.Unmarshal(result, &payload)
	e.result = &vmcp.InputRequiredResult{
		InputRequests: payload.InputRequests,
		RequestState:  payload.RequestState,
	}
	return e
}

// InputRequiredFromError extracts the SEP-2322 input_required payload from an
// error chain, however deeply the backend client wrapped it. It reports false
// for errors that are not an input_required round — including the
// unrecognized-resultType variant of the same sentinel — so callers can use
// it as the single MRTR branch point:
//
//	if round, ok := client.InputRequiredFromError(err); ok { ... relay round ... }
//
// This is the seam the ingress half (dispatchModern's input_required envelope
// and the Legacy-client bridge; docs/arch/16-vmcp-mrtr.md slices 2-4) consumes.
func InputRequiredFromError(err error) (*vmcp.InputRequiredResult, bool) {
	var ire *inputRequiredError
	if errors.As(err, &ire) && ire.result != nil {
		return ire.result, true
	}
	return nil, false
}
