// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

// ValidateJSONRPCResponseBody strictly validates a JSON-RPC 2.0 response body.
// Unlike ParseMCPResponse, this is an enforcement helper: malformed bodies return
// an error instead of being treated as "no application error".
func ValidateJSONRPCResponseBody(body []byte) error {
	var payload any
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&payload); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON-RPC response must contain a single JSON value")
	}

	switch value := payload.(type) {
	case map[string]any:
		return validateJSONRPCResponseObject(value)
	case []any:
		if len(value) == 0 {
			return fmt.Errorf("JSON-RPC batch response must not be empty")
		}
		for i, item := range value {
			obj, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("JSON-RPC batch item %d must be an object", i)
			}
			if err := validateJSONRPCResponseObject(obj); err != nil {
				return fmt.Errorf("JSON-RPC batch item %d is invalid: %w", i, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("JSON-RPC response must be an object or array")
	}
}

func validateJSONRPCResponseObject(obj map[string]any) error {
	if obj["jsonrpc"] != "2.0" {
		return fmt.Errorf(`JSON-RPC response must include "jsonrpc":"2.0"`)
	}

	if _, ok := obj["id"]; !ok {
		return fmt.Errorf("JSON-RPC response must include id")
	}
	if !isValidJSONRPCID(obj["id"]) {
		return fmt.Errorf("JSON-RPC response id must be string, number, or null")
	}

	_, hasResult := obj["result"]
	_, hasError := obj["error"]
	if hasResult == hasError {
		return fmt.Errorf("JSON-RPC response must include exactly one of result or error")
	}
	if hasError {
		if errObj, ok := obj["error"].(map[string]any); !ok || !isValidJSONRPCError(errObj) {
			return fmt.Errorf("JSON-RPC error response must include error.code and error.message")
		}
	}

	return nil
}

func isValidJSONRPCID(id any) bool {
	switch id.(type) {
	case nil, string, float64:
		return true
	default:
		return false
	}
}

func isValidJSONRPCError(errObj map[string]any) bool {
	code, codeOK := errObj["code"].(float64)
	if !codeOK || math.Trunc(code) != code {
		return false
	}
	_, messageOK := errObj["message"].(string)
	return messageOK
}
