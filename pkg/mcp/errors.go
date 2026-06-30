// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

// CodedError is implemented by domain errors that should surface a stable error
// code and optional structured data in an MCP tool result.
//
// The mcp-go tool-handler seam maps returned Go errors to a generic JSON-RPC
// INTERNAL_ERROR, so Serve-path handlers convert these errors to IsError tool
// results with StructuredContent instead of returning them as handler errors.
type CodedError interface {
	error
	Code() int64
	Data() map[string]any
}
