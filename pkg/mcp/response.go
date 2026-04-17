// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
)

// ParsedMCPResponse contains the result of inspecting a JSON-RPC response
// body for application-level errors. Only the error-related fields are
// extracted; the full result payload is intentionally not captured to avoid
// duplicating the privacy-sensitive IncludeResponseData path.
type ParsedMCPResponse struct {
	// HasError is true when the response body contains a top-level "error" field.
	HasError bool
	// ErrorCode is the JSON-RPC error code (e.g., -32603 for internal error).
	ErrorCode int
	// ErrorMessage is the raw error message from the JSON-RPC response.
	ErrorMessage string
}

// jsonRPCError mirrors the JSON-RPC 2.0 error object for decoding purposes.
// We use a minimal custom struct rather than jsonrpc2.DecodeMessage because
// the library's wireError type is unexported, making it impossible to extract
// the numeric error code from outside the package.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonRPCResponseEnvelope is the minimal structure needed to detect a
// JSON-RPC error in a response body. We intentionally omit "result" to
// keep the parse lightweight.
type jsonRPCResponseEnvelope struct {
	Error *jsonRPCError `json:"error,omitempty"`
}

// ParseMCPResponse inspects a response body and returns a ParsedMCPResponse
// indicating whether it contains a JSON-RPC error. The function is
// intentionally lenient: if the body is not valid JSON or does not contain
// an "error" field, it returns HasError=false rather than an error.
func ParseMCPResponse(body []byte) *ParsedMCPResponse {
	if len(body) == 0 {
		return &ParsedMCPResponse{}
	}

	var envelope jsonRPCResponseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return &ParsedMCPResponse{}
	}

	if envelope.Error == nil {
		return &ParsedMCPResponse{}
	}

	return &ParsedMCPResponse{
		HasError:     true,
		ErrorCode:    envelope.Error.Code,
		ErrorMessage: envelope.Error.Message,
	}
}
