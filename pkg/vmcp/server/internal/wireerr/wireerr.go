// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package wireerr builds and recognizes the JSON-RPC errors that vMCP's tool
// handlers return to the MCP SDK to put on the wire as-is. It is the only vMCP
// package that imports go-sdk's jsonrpc package, so the SDK rule below lives
// in one place.
//
// The rule: go-sdk puts a handler's error on the wire with its code and data
// only when the error is a bare *jsonrpc.Error. For a wrapped one it keeps the
// code but drops the data. A handler must therefore return what URLElicitationRequired
// builds, or what FindURLElicitationRequired finds, without wrapping it.
package wireerr

import (
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// urlElicitationRequiredMessage is the -32042 error message. The elicitation
// messages travel in data.elicitations, so the top-level message stays fixed.
const urlElicitationRequiredMessage = "URL elicitation required"

// URLElicitationRequired returns the MCP 2025-11-25 URLElicitationRequiredError
// (code -32042) carrying data, which the caller builds as
// {"elicitations": [...]}. Return it from a tool handler unwrapped.
func URLElicitationRequired(data json.RawMessage) error {
	return &jsonrpc.Error{
		Code:    mcp.URL_ELICITATION_REQUIRED,
		Message: urlElicitationRequiredMessage,
		Data:    data,
	}
}

// FindURLElicitationRequired returns the bare -32042 error in err's chain, or
// nil when there is none. A handler that receives a wrapped -32042 from an
// inner handler returns the result unwrapped, so the data reaches the wire.
func FindURLElicitationRequired(err error) error {
	var wireErr *jsonrpc.Error
	if errors.As(err, &wireErr) && wireErr.Code == mcp.URL_ELICITATION_REQUIRED {
		return wireErr
	}
	return nil
}
