// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"math"
	"time"

	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
)

const (
	// CodeRateLimited is the JSON-RPC error code for rate-limited requests,
	// mirroring HTTP 429 (Too Many Requests) the same way mcp.JSONRPCCodeDenied
	// mirrors 403. It sits outside the JSON-RPC reserved range
	// (-32768..-32000) so a single value is conformant on every MCP revision:
	// 2026-07-28 reserves -32020..-32099 exclusively for spec-defined codes
	// and directs application codes outside the reserved range, while
	// 2025-11-25 and earlier inherit plain JSON-RPC 2.0, which leaves the
	// space outside that range application-defined. Replaces -32029, chosen
	// by RFC THV-0057 when -32000..-32099 was uniformly implementation-defined
	// — a premise the 2026-07-28 partition invalidated (#6101).
	CodeRateLimited int64 = 429

	// MessageRateLimited is the error message for rate-limited requests.
	MessageRateLimited = "Rate limit exceeded"
)

// RateLimitedError reports that a request exceeded its configured rate limit.
type RateLimitedError struct {
	RetryAfter time.Duration
}

var _ thvmcp.CodedError = (*RateLimitedError)(nil)

func (*RateLimitedError) Error() string {
	return MessageRateLimited
}

// Code returns the ToolHive JSON-RPC-compatible code for rate-limited requests.
func (*RateLimitedError) Code() int64 {
	return CodeRateLimited
}

// Data returns structured retry metadata for transport adapters that cannot
// emit a custom JSON-RPC error object from the tool-handler seam.
func (e *RateLimitedError) Data() map[string]any {
	return map[string]any{
		"retryAfterSeconds": int(math.Ceil(e.RetryAfter.Seconds())),
	}
}
