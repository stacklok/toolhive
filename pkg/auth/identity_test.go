// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaimsToIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claims    jwt.MapClaims
		token     string
		wantErr   bool
		errMsg    string
		checkFunc func(t *testing.T, identity *Identity)
	}{
		{
			name: "valid_oidc_claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "John Doe",
				"email": "john@example.com",
			},
			token:   "test-token",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				t.Helper()

				assert.Equal(t, "user123", identity.Subject)
				assert.Equal(t, "John Doe", identity.Name)
				assert.Equal(t, "john@example.com", identity.Email)
				assert.Equal(t, "test-token", identity.Token)
				assert.Equal(t, "Bearer", identity.TokenType)
				assert.Empty(t, identity.Groups, "Groups should not be populated")
			},
		},
		{
			name: "minimal_claims_only_sub",
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			token:   "",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				t.Helper()

				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Name)
				assert.Empty(t, identity.Email)
				assert.Empty(t, identity.Token)
			},
		},
		{
			name: "missing_sub_claim",
			claims: jwt.MapClaims{
				"name":  "John Doe",
				"email": "john@example.com",
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "empty_sub_claim",
			claims: jwt.MapClaims{
				"sub": "",
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "non_string_sub_claim",
			claims: jwt.MapClaims{
				"sub": 12345,
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "groups_claim_not_populated",
			claims: jwt.MapClaims{
				"sub":    "user123",
				"groups": []string{"admin", "developers"},
			},
			token:   "test-token",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				t.Helper()

				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Groups, "Groups should not be auto-populated")
				assert.Contains(t, identity.Claims, "groups", "groups claim should be in Claims map")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			identity, err := claimsToIdentity(tt.claims, tt.token)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, identity)
			} else {
				require.NoError(t, err)
				require.NotNil(t, identity)
				if tt.checkFunc != nil {
					tt.checkFunc(t, identity)
				}
			}
		})
	}
}

func TestIdentity_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *Identity
		want     string
	}{
		{
			name: "normal_identity",
			identity: &Identity{
				Subject: "user123",
				Name:    "Alice",
				Token:   "secret-token",
			},
			want: `Identity{Subject:"user123"}`,
		},
		{
			name:     "nil_identity",
			identity: nil,
			want:     "<nil>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.identity.String()
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestIdentity_MarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		identity  *Identity
		wantErr   bool
		checkFunc func(t *testing.T, data []byte)
	}{
		{
			name: "redacts_token",
			identity: &Identity{
				Subject:   "user123",
				Name:      "Alice",
				Email:     "alice@example.com",
				Token:     "secret-token",
				TokenType: "Bearer",
				Claims: map[string]any{
					"org_id": "org456",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				assert.Equal(t, "user123", result["subject"])
				assert.Equal(t, "Alice", result["name"])
				assert.Equal(t, "alice@example.com", result["email"])
				assert.Equal(t, "REDACTED", result["token"])
				assert.Equal(t, "Bearer", result["tokenType"])
				assert.NotContains(t, string(data), "secret-token")
			},
		},
		{
			name: "empty_token_not_redacted",
			identity: &Identity{
				Subject: "user123",
				Token:   "",
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				assert.Equal(t, "", result["token"])
			},
		},
		{
			name:     "nil_identity",
			identity: nil,
			wantErr:  false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()
				assert.Equal(t, "null", string(data))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := tt.identity.MarshalJSON()

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, data)
				}
			}
		})
	}
}
