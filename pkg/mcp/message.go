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
	// MCP permits an error response without an ID. The decoder requires one;
	// supply it only for decoding and restore the absent ID below.
	if envelope["error"] != nil && !id.IsValid() {
		body = append([]byte(`{"id":0,`), body[1:]...)
	}
	msg, err = jsonrpc2.DecodeMessage(body)
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
	switch {
	case errors.Is(err, jsonrpc2.ErrParse):
		return "parse"
	case errors.As(err, &batch):
		return "batch"
	default:
		return "invalid_request"
	}
}

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
