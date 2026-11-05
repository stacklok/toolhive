// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasContentEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "absent", want: false},
		{name: "identity", values: []string{"identity"}, want: false},
		{name: "identity in any case", values: []string{" IDENTITY "}, want: false},
		{name: "gzip", values: []string{"gzip"}, want: true},
		{name: "br after identity in one value", values: []string{"identity, br"}, want: true},
		{name: "zstd in a second header", values: []string{"identity", "zstd"}, want: true},
		{name: "empty tokens", values: []string{" , "}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for _, v := range tt.values {
				h.Add("Content-Encoding", v)
			}
			assert.Equal(t, tt.want, HasContentEncoding(h))
		})
	}
}

func TestDeleteEntityHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	for _, name := range staleEntityHeaders {
		h.Set(name, "x")
	}
	h.Set("Content-Type", "application/json")
	h.Set("Mcp-Session-Id", "s")

	DeleteEntityHeaders(h)

	for _, name := range staleEntityHeaders {
		assert.Empty(t, h.Values(name), name)
	}
	assert.Equal(t, "application/json", h.Get("Content-Type"))
	assert.Equal(t, "s", h.Get("Mcp-Session-Id"))
}
