// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJSONRPCCodeForStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   int64
	}{
		{name: "forbidden maps to denied", status: http.StatusForbidden, want: JSONRPCCodeDenied},
		{name: "request entity too large maps to invalid request", status: http.StatusRequestEntityTooLarge, want: CodeInvalidRequest},
		{name: "unprocessable entity maps to denied", status: http.StatusUnprocessableEntity, want: JSONRPCCodeDenied},
		{name: "internal server error falls through to internal error", status: http.StatusInternalServerError, want: CodeInternalError},
		{name: "unmapped status defaults to internal error", status: http.StatusBadGateway, want: CodeInternalError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, JSONRPCCodeForStatus(tt.status))
		})
	}
}
