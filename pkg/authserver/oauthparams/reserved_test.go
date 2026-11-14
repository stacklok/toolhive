// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthparams

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTokenParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  map[string]string
		wantErr string
	}{
		{
			name:   "nil params",
			params: nil,
		},
		{
			name:   "unreserved params pass",
			params: map[string]string{"audience": "https://api.example.com"},
		},
		{
			name:    "framework-managed grant_type is rejected",
			params:  map[string]string{"grant_type": "client_credentials"},
			wantErr: `reserved token parameter "grant_type"`,
		},
		{
			// RFC 7523 client-authentication credentials: allowing these
			// would combine an assertion with the configured client_secret
			// or Basic credentials into an invalid multi-method request.
			name:    "client_assertion is reserved",
			params:  map[string]string{"client_assertion": "ey.j.w.t"},
			wantErr: `reserved token parameter "client_assertion"`,
		},
		{
			name:    "client_assertion_type is reserved",
			params:  map[string]string{"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			wantErr: `reserved token parameter "client_assertion_type"`,
		},
		{
			name:   "RFC 8707 resource indicator passes",
			params: map[string]string{"resource": "https://api.example.com/v1"},
		},
		{
			name:    "empty resource is rejected",
			params:  map[string]string{"resource": ""},
			wantErr: `must not be empty`,
		},
		{
			name:    "relative resource is rejected",
			params:  map[string]string{"resource": "/api/v1"},
			wantErr: `must be an absolute URI`,
		},
		{
			name:    "resource with a fragment is rejected",
			params:  map[string]string{"resource": "https://api.example.com/v1#frag"},
			wantErr: `must not contain a fragment`,
		},
		{
			name:    "resource with an empty fragment is rejected",
			params:  map[string]string{"resource": "https://api.example.com/v1#"},
			wantErr: `must not contain a fragment`,
		},
		{
			name:    "malformed resource is rejected",
			params:  map[string]string{"resource": "https://api.example.com/%zz"},
			wantErr: `invalid URL escape`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateTokenParams(tt.params)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
