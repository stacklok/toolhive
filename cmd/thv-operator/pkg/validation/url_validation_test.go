// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestValidateRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rawURL      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid https URL",
			rawURL:  "https://mcp.example.com",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			rawURL:  "http://mcp.example.com",
			wantErr: false,
		},
		{
			name:        "empty URL",
			rawURL:      "",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "no scheme",
			rawURL:      "mcp.example.com",
			wantErr:     true,
			errContains: "scheme",
		},
		{
			name:        "unsupported scheme",
			rawURL:      "ftp://mcp.example.com",
			wantErr:     true,
			errContains: "scheme",
		},
		{
			name:        "missing host",
			rawURL:      "https://",
			wantErr:     true,
			errContains: "host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateRemoteURL(tt.rawURL)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateJWKSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rawURL      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "empty URL allowed",
			rawURL:  "",
			wantErr: false,
		},
		{
			name:    "valid https URL",
			rawURL:  "https://jwks.example.com/.well-known/jwks.json",
			wantErr: false,
		},
		{
			name:        "http rejected",
			rawURL:      "http://jwks.example.com",
			wantErr:     true,
			errContains: "HTTPS",
		},
		{
			name:        "unsupported scheme",
			rawURL:      "ftp://jwks.example.com",
			wantErr:     true,
			errContains: "HTTPS",
		},
		{
			name:        "missing host",
			rawURL:      "https://",
			wantErr:     true,
			errContains: "host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateJWKSURL(tt.rawURL)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
