// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"time"

	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
)

const (
	// CodeRateLimited is the JSON-RPC error code for rate-limited requests.
	// Per RFC THV-0057: implementation-defined code in the -32000 to -32099 range.
	CodeRateLimited int64 = -32029

	// MessageRateLimited is the error message for rate-limited requests.
	MessageRateLimited = "Rate limit exceeded"
)

// RateLimitedError reports that a request exceeded its configured rate limit.
type RateLimitedError struct {
	RetryAfter time.Duration
}

var _ thvmcp.RequestError = (*RateLimitedError)(nil)

func (*RateLimitedError) Error() string {
	return MessageRateLimited
}

// MCPRequestError marks rate-limit denials as request-level failures rather
// than tool execution errors.
func (*RateLimitedError) MCPRequestError() {}
