// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"maps"
)

// ValidateResponse reports whether body is a single, structurally valid MCP
// JSON-RPC response. It applies DecodeMessage's envelope rules for responses:
// a leading UTF-8 BOM and JSON whitespace are accepted, ids must be strings or
// signed int64 integers, an error response may omit its id (MCP permits one),
// and exactly one of result or error must be present with a well-formed error
// object; an explicit null for the unused member is treated as absent (see
// withoutNullAbsentMember). It rejects invalid JSON (including an empty body),
// batches, requests and notifications, null or fractional ids, and envelopes
// that repeat a member or alias a reserved one by case.
//
// Unlike DecodeMessage, it checks the envelope only. Member names inside
// result and error.data are not checked for case-fold collisions: a result is
// opaque to ToolHive, and JSON permits case-distinct names there (a tool's
// input schema may define both "ID" and "id"). It does not compare the id to
// the request being answered.
//
// It does not log: the caller rejecting the response logs once. The returned
// error's Error() is a fixed reason ("parse", "batch", "not_a_response" or
// "invalid_response") that never reflects body content, so it is safe to log
// and to use as a metric label.
func ValidateResponse(body []byte) error {
	body = bytes.Trim(bytes.TrimPrefix(body, UTF8BOM), " \t\r\n")
	if !json.Valid(body) {
		return errResponseParse
	}
	if IsBatchRequest(body) {
		return errResponseBatch
	}
	var envelope map[string]json.RawMessage
	if body[0] != '{' || json.Unmarshal(body, &envelope) != nil {
		return errInvalidResponse
	}
	if _, isRequest := envelope["method"]; isRequest {
		return errNotAResponse
	}
	if reservedMemberAlias(envelope, "jsonrpc", "id", "method", "params", "result", "error") ||
		hasDuplicateEnvelopeMembers(body) || !validMessageEnvelope(withoutNullAbsentMember(envelope)) {
		return errInvalidResponse
	}
	if _, err := decodeMessageID(envelope["id"]); err != nil {
		return errInvalidResponse
	}
	return nil
}

// withoutNullAbsentMember returns envelope without a result or error member
// that is an explicit null standing in for the absent one. Serializers that do
// not omit nulls emit {"result":{},"error":null}, or "result":null beside an
// error, and lenient clients accept both. JSON-RPC requires the unused member
// to be absent, but these responses are unambiguous, so they are accepted
// rather than rejected. A null error is never a valid error object, so it is
// always dropped; a null result is a valid result, so it is dropped only when
// an error is present. envelope itself is not modified.
func withoutNullAbsentMember(envelope map[string]json.RawMessage) map[string]json.RawMessage {
	isNull := func(raw json.RawMessage) bool { return raw != nil && string(raw) == "null" }
	dropError := isNull(envelope["error"])
	dropResult := !dropError && envelope["error"] != nil && isNull(envelope["result"])
	if !dropError && !dropResult {
		return envelope
	}
	out := maps.Clone(envelope)
	if dropError {
		delete(out, "error")
	} else {
		delete(out, "result")
	}
	return out
}

// responseValidationError is a fixed, non-reflective rejection reason.
type responseValidationError string

func (e responseValidationError) Error() string { return string(e) }

const (
	errResponseParse   responseValidationError = "parse"
	errResponseBatch   responseValidationError = "batch"
	errNotAResponse    responseValidationError = "not_a_response"
	errInvalidResponse responseValidationError = "invalid_response"
)

// hasDuplicateEnvelopeMembers reports whether the top-level object of a valid
// JSON object body repeats a member name. Unmarshaling into a map keeps only
// the last occurrence, while other parsers may keep the first, so a repeated
// id or result could be read differently by the client. Nested values are
// skipped without being inspected.
func hasDuplicateEnvelopeMembers(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if _, err := decoder.Token(); err != nil {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if _, dup := seen[key]; dup {
			return true
		}
		seen[key] = struct{}{}
		if !skipJSONValue(decoder) {
			return false
		}
	}
	return false
}

// skipJSONValue consumes the next complete JSON value from decoder without
// retaining it.
func skipJSONValue(decoder *json.Decoder) bool {
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return true
		}
	}
}
