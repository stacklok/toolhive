// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package legacyhint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const legacyJSON = `{
  "version": "1.0.0",
  "servers": {"example": {"image": "example/srv:latest"}}
}`

const upstreamJSON = `{
  "version": "1.0.0",
  "data": {"servers": []}
}`

func TestIsUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "upstream format with data wrapper", in: upstreamJSON, want: true},
		{name: "legacy format without data wrapper", in: legacyJSON, want: false},
		{name: "empty input", in: "", want: false},
		{name: "invalid JSON", in: "not json", want: false},
		{name: "data field is array, not object", in: `{"data": []}`, want: false},
		{name: "data field is null", in: `{"data": null}`, want: false},
		{name: "no data field", in: `{"version": "1.0"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsUpstream([]byte(tt.in)))
		})
	}
}

func TestLooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "top-level servers", in: legacyJSON, want: true},
		{name: "top-level remote_servers", in: `{"remote_servers": {"r": {}}}`, want: true},
		{name: "top-level groups", in: `{"groups": [{"name": "g"}]}`, want: true},
		{name: "upstream wraps under data", in: upstreamJSON, want: false},
		{name: "empty object", in: `{}`, want: false},
		{name: "malformed JSON returns false", in: "not json", want: false},
		{name: "empty input returns false", in: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, Looks([]byte(tt.in)))
		})
	}
}
