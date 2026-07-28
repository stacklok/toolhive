// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimitedErrorCodeIsApplicationDefined(t *testing.T) {
	t.Parallel()

	const (
		jsonRPCReservedMin = -32768
		jsonRPCReservedMax = -32000
		mcpReservedMin     = -32099
		mcpReservedMax     = -32020
	)

	assert.True(t,
		CodeRateLimited < int64(jsonRPCReservedMin) || CodeRateLimited > int64(jsonRPCReservedMax),
		"rate-limit code must stay outside the JSON-RPC reserved range",
	)
	assert.False(t,
		CodeRateLimited >= int64(mcpReservedMin) && CodeRateLimited <= int64(mcpReservedMax),
		"rate-limit code must stay outside the MCP reserved sub-range",
	)

	limited := &RateLimitedError{RetryAfter: 1500 * time.Millisecond}
	assert.Equal(t, CodeRateLimited, limited.Code())
	assert.Equal(t, map[string]any{"retryAfterSeconds": 2}, limited.Data())
}
