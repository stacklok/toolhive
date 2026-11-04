// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/exp/jsonrpc2"
)

// DecodeMessage admits and decodes a single, unambiguous MCP JSON-RPC envelope,
// independently of HTTP headers. IDs must be strings or signed int64 JSON integers
// and are preserved exactly. Duplicate or case-fold-colliding object keys are
// rejected recursively, including in params, results, errors and extensions.
// Unknown extension members are allowed; aliases of reserved members are not.
// Request and notification params that do not decode as an object (e.g. a number
// outside float64 range) are rejected.
// Result values retain all accepted JSON shapes; no result-object schema is imposed.
// Only requests and notifications should be published as ParsedMCPRequest.
//
// Invalid JSON returns jsonrpc2.ErrParse; valid batches return *BatchUnsupportedError;
// invalid envelopes, ambiguous members and unsupported IDs return a CodedError
// with CodeInvalidRequest. A leading UTF-8 BOM and JSON whitespace are accepted.
func DecodeMessage(body []byte) (msg jsonrpc2.Message, err error) {
	defer func() {
		if err != nil {
			slog.Warn("rejected invalid MCP JSON-RPC message", "reason", messageRejectionReason(err))
		}
	}()
	body = bytes.Trim(bytes.TrimPrefix(body, UTF8BOM), " \t\r\n")
	if !json.Valid(body) {
		return nil, jsonrpc2.ErrParse
	}
	if IsBatchRequest(body) {
		return nil, &BatchUnsupportedError{}
	}
	var envelope map[string]json.RawMessage
	if body[0] != '{' || json.Unmarshal(body, &envelope) != nil ||
		hasAmbiguousJSONMembers(body) || reservedMemberAlias(envelope, "jsonrpc", "id", "method", "params", "result", "error") {
		return nil, ambiguousRequest
	}
	if !validMessageEnvelope(envelope) {
		return nil, ambiguousRequest
	}
	id, err := decodeMessageID(envelope["id"])
	if err != nil {
		return nil, err
	}
	if _, isRequest := envelope["method"]; isRequest && !decodableParams(envelope["params"]) {
		return nil, newUndecodableParams(id)
	}
	return decodeWithExactID(body, envelope, id)
}

// decodeWithExactID decodes an admitted envelope, preserving id exactly.
func decodeWithExactID(body []byte, envelope map[string]json.RawMessage, id jsonrpc2.ID) (jsonrpc2.Message, error) {
	// MCP permits an error response without an ID. The decoder requires one;
	// supply it only for decoding and restore the absent ID below.
	if envelope["error"] != nil && !id.IsValid() {
		body = append([]byte(`{"id":0,`), body[1:]...)
	}
	msg, err := jsonrpc2.DecodeMessage(body)
	if err != nil {
		return nil, ambiguousRequest
	}
	// DecodeMessage rounds numeric IDs through float64. Restore the exact ID.
	switch message := msg.(type) {
	case *jsonrpc2.Request:
		message.ID = id
	case *jsonrpc2.Response:
		message.ID = id
	}
	return msg, nil
}

func messageRejectionReason(err error) string {
	var batch *BatchUnsupportedError
	var undecodable *undecodableParamsError
	switch {
	case errors.Is(err, jsonrpc2.ErrParse):
		return "parse"
	case errors.As(err, &batch):
		return "batch"
	case errors.As(err, &undecodable):
		return "undecodable_params"
	default:
		return "invalid_request"
	}
}

// undecodableParamsError rejects a request whose envelope is otherwise valid
// but whose params do not decode. Only then is the request ID unambiguous, so
// it is carried to let the client correlate the error. Error is fixed so no
// request content is reflected.
type undecodableParamsError struct{ id any }

// newUndecodableParams carries id only when it is valid (a notification has none).
func newUndecodableParams(id jsonrpc2.ID) *undecodableParamsError {
	if !id.IsValid() {
		return &undecodableParamsError{}
	}
	return &undecodableParamsError{id: id.Raw()}
}

func (*undecodableParamsError) Error() string { return "Invalid Request" }

// Code implements CodedError.
func (*undecodableParamsError) Code() int64 { return CodeInvalidRequest }

// Data implements CodedError.
func (*undecodableParamsError) Data() map[string]any { return nil }

// RequestID returns the decoded request ID (string or int64), or nil.
func (e *undecodableParamsError) RequestID() any { return e.id }

// writeBodyReadError never exposes reader-provided errors to clients or logs.
func writeBodyReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		slog.Warn("rejected unreadable MCP request body", "reason", "body_too_large")
		http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
		return
	}
	slog.Warn("rejected unreadable MCP request body", "reason", "read_error")
	WriteClassificationError(w, nil, ambiguousRequest)
}

func decodeMessageID(raw json.RawMessage) (jsonrpc2.ID, error) {
	if raw == nil {
		return jsonrpc2.ID{}, nil
	}
	if raw[0] == '"' {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			return jsonrpc2.StringID(id), nil
		}
	} else {
		var id int64
		if json.Unmarshal(raw, &id) == nil && string(raw) != "null" {
			return jsonrpc2.Int64ID(id), nil
		}
	}
	return jsonrpc2.ID{}, ambiguousRequest
}

func validMessageEnvelope(envelope map[string]json.RawMessage) bool {
	var version string
	if json.Unmarshal(envelope["jsonrpc"], &version) != nil || version != "2.0" {
		return false
	}
	if method, request := envelope["method"]; request {
		var name string
		params := envelope["params"]
		return json.Unmarshal(method, &name) == nil && name != "" &&
			envelope["result"] == nil && envelope["error"] == nil && (params == nil || params[0] == '{')
	}
	return validResponseEnvelope(envelope)
}

// decodableParams reports whether request params decode the way the parser
// decodes them. Syntactically valid JSON can still fail to decode: a number
// outside float64 range (e.g. 1e1000) is accepted by lenient backend parsers
// but makes the decode fail, which would leave authorization, rate limiting and
// audit acting on an empty resource ID while the backend executes the real one.
// Such requests are rejected rather than decoded with json.Number, because
// authorizers only consume float64 numeric arguments. Responses are not checked:
// their results are opaque to ToolHive and may legitimately carry any number.
func decodableParams(params json.RawMessage) bool {
	_, err := decodeParams(params)
	return err == nil
}

// decodeParams decodes request params into an object map. It is the single
// decode shared by admission (decodableParams) and the parser, so the two
// cannot disagree about which params are usable. Nil params decode to a nil map.
func decodeParams(params json.RawMessage) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func validResponseEnvelope(envelope map[string]json.RawMessage) bool {
	result, rpcError := envelope["result"], envelope["error"]
	if envelope["params"] != nil || (result == nil) == (rpcError == nil) {
		return false
	}
	if rpcError != nil {
		return validMessageError(rpcError)
	}
	return envelope["id"] != nil
}

func validMessageError(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if raw[0] != '{' || json.Unmarshal(raw, &fields) != nil || reservedMemberAlias(fields, "code", "message", "data") {
		return false
	}
	var code int64
	var message string
	return fields["code"] != nil && string(fields["code"]) != "null" && json.Unmarshal(fields["code"], &code) == nil &&
		fields["message"] != nil && string(fields["message"]) != "null" && json.Unmarshal(fields["message"], &message) == nil
}

func reservedMemberAlias(fields map[string]json.RawMessage, reserved ...string) bool {
	for key := range fields {
		for _, name := range reserved {
			if key != name && strings.EqualFold(key, name) {
				return true
			}
		}
	}
	return false
}
