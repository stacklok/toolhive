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
				PrincipalInfo: PrincipalInfo{
					Subject: "user123",
					Name:    "Alice",
				},
				Token: "secret-token",
			},
			want: `Identity{Subject:"user123"}`,
		},
		{
			name:     "nil_identity",
			identity: nil,
			want:     "<nil>",
		},
		{
			name: "does_not_leak_upstream_tokens",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{Subject: "user123"},
				UpstreamTokens: map[string]string{
					"github": "gho_secret123",
				},
			},
			want: `Identity{Subject:"user123"}`,
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

func TestIdentity_GetPrincipalInfo(t *testing.T) {
	t.Parallel()

	t.Run("projects_non_sensitive_fields", func(t *testing.T) {
		t.Parallel()

		identity := &Identity{
			PrincipalInfo: PrincipalInfo{
				Subject: "user123",
				Name:    "Alice",
				Email:   "alice@example.com",
				Groups:  []string{"admins"},
				Claims:  map[string]any{"org_id": "org456"},
			},
			Token:     "secret-token",
			TokenType: "Bearer",
			Metadata:  map[string]string{"source": "oidc"},
		}

		pi := identity.GetPrincipalInfo()

		require.NotNil(t, pi)
		assert.Equal(t, "user123", pi.Subject)
		assert.Equal(t, "Alice", pi.Name)
		assert.Equal(t, "alice@example.com", pi.Email)
		assert.Equal(t, []string{"admins"}, pi.Groups)
		assert.Equal(t, map[string]any{"org_id": "org456"}, pi.Claims)

		// Verify token/tokenType/metadata are structurally absent.
		data, err := json.Marshal(pi)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "token")
		assert.NotContains(t, string(data), "tokenType")
		assert.NotContains(t, string(data), "metadata")
		assert.NotContains(t, string(data), "secret-token")
	})

	t.Run("nil_identity", func(t *testing.T) {
		t.Parallel()

		var identity *Identity
		pi := identity.GetPrincipalInfo()
		assert.Nil(t, pi)
	})

	t.Run("minimal_identity", func(t *testing.T) {
		t.Parallel()

		identity := &Identity{PrincipalInfo: PrincipalInfo{Subject: "user1"}}
		pi := identity.GetPrincipalInfo()

		require.NotNil(t, pi)
		assert.Equal(t, "user1", pi.Subject)

		// Verify omitempty: empty fields should not appear in JSON.
		data, err := json.Marshal(pi)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "name")
		assert.NotContains(t, string(data), "email")
		assert.NotContains(t, string(data), "groups")
		assert.NotContains(t, string(data), "claims")
	})
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
				PrincipalInfo: PrincipalInfo{
					Subject: "user123",
					Name:    "Alice",
					Email:   "alice@example.com",
					Claims: map[string]any{
						"org_id": "org456",
					},
				},
				Token:     "secret-token",
				TokenType: "Bearer",
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
				PrincipalInfo: PrincipalInfo{
					Subject: "user123",
				},
				Token: "",
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
		{
			name: "redacts_upstream_tokens",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{Subject: "user123"},
				UpstreamTokens: map[string]string{
					"github":    "gho_secret123",
					"atlassian": "atl_secret456",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				tokens, ok := result["upstreamTokens"].(map[string]any)
				require.True(t, ok, "upstreamTokens should be a map")
				assert.Equal(t, "REDACTED", tokens["github"])
				assert.Equal(t, "REDACTED", tokens["atlassian"])
				assert.NotContains(t, string(data), "gho_secret123")
				assert.NotContains(t, string(data), "atl_secret456")
			},
		},
		{
			name: "empty_upstream_tokens_omitted",
			identity: &Identity{
				PrincipalInfo:  PrincipalInfo{Subject: "user123"},
				UpstreamTokens: map[string]string{},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				// Empty map should be omitted because len() == 0 produces nil redacted map
				_, exists := result["upstreamTokens"]
				assert.False(t, exists, "empty upstreamTokens should be omitted")
			},
		},
		{
			name: "nil_upstream_tokens_omitted",
			identity: &Identity{
				PrincipalInfo:  PrincipalInfo{Subject: "user123"},
				UpstreamTokens: nil,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				_, exists := result["upstreamTokens"]
				assert.False(t, exists, "nil upstreamTokens should be omitted")
			},
		},
		{
			name: "upstream_tokens_mixed_empty_and_populated",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{Subject: "user123"},
				UpstreamTokens: map[string]string{
					"github":  "gho_secret123",
					"pending": "",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				tokens, ok := result["upstreamTokens"].(map[string]any)
				require.True(t, ok, "upstreamTokens should be a map")
				assert.Equal(t, "REDACTED", tokens["github"])
				assert.Equal(t, "", tokens["pending"])
				assert.NotContains(t, string(data), "gho_secret123")
			},
		},
		{
			name: "redacts_upstream_id_tokens",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{Subject: "user123"},
				UpstreamIDTokens: map[string]string{
					"github":    "eyJhbGciOiJSUzI1NiJ9.github-id-token",
					"atlassian": "eyJhbGciOiJSUzI1NiJ9.atlassian-id-token",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				tokens, ok := result["upstreamIDTokens"].(map[string]any)
				require.True(t, ok, "upstreamIDTokens should be a map")
				assert.Equal(t, "REDACTED", tokens["github"])
				assert.Equal(t, "REDACTED", tokens["atlassian"])
				assert.NotContains(t, string(data), "eyJhbGciOiJSUzI1NiJ9")
			},
		},
		{
			name: "empty_upstream_id_tokens_omitted",
			identity: &Identity{
				PrincipalInfo:    PrincipalInfo{Subject: "user123"},
				UpstreamIDTokens: map[string]string{},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				_, exists := result["upstreamIDTokens"]
				assert.False(t, exists, "empty upstreamIDTokens should be omitted")
			},
		},
		{
			name: "nil_upstream_id_tokens_omitted",
			identity: &Identity{
				PrincipalInfo:    PrincipalInfo{Subject: "user123"},
				UpstreamIDTokens: nil,
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				err := json.Unmarshal(data, &result)
				require.NoError(t, err)

				_, exists := result["upstreamIDTokens"]
				assert.False(t, exists, "nil upstreamIDTokens should be omitted")
			},
		},
		{
			name: "upstream_id_tokens_round_trip_preserves_keys_redacts_values",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{Subject: "user123"},
				UpstreamIDTokens: map[string]string{
					"github":    "eyJhbGciOiJSUzI1NiJ9.github-id-token",
					"atlassian": "eyJhbGciOiJSUzI1NiJ9.atlassian-id-token",
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				// Unmarshal into the same SafeIdentity shape that MarshalJSON produces
				// so we can verify the round-trip preserves the key set and the
				// redaction replaces every non-empty value with "REDACTED".
				var roundTrip struct {
					Subject          string            `json:"subject"`
					UpstreamIDTokens map[string]string `json:"upstreamIDTokens,omitempty"`
				}
				require.NoError(t, json.Unmarshal(data, &roundTrip))

				assert.Equal(t, "user123", roundTrip.Subject)
				assert.Equal(t, map[string]string{
					"github":    "REDACTED",
					"atlassian": "REDACTED",
				}, roundTrip.UpstreamIDTokens,
					"round-trip must preserve all provider keys with redacted values")
				assert.NotContains(t, string(data), "eyJhbGciOiJSUzI1NiJ9",
					"raw ID token material must not appear in marshaled output")
			},
		},
		{
			name: "strips_tsid_claim_from_serialized_claims",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{
					Subject: "user123",
					Claims: map[string]any{
						"tsid":   "session-xyz",
						"sub":    "user123",
						"org_id": "org456",
					},
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				require.NoError(t, json.Unmarshal(data, &result))

				claims, ok := result["claims"].(map[string]any)
				require.True(t, ok, "claims should be a map")
				_, hasTsid := claims["tsid"]
				assert.False(t, hasTsid, "tsid must be stripped from serialized claims")
				assert.NotContains(t, string(data), "session-xyz",
					"tsid value must not appear anywhere in marshaled output")
				// Guard: co-resident claims must be preserved
				assert.Equal(t, "user123", claims["sub"], "sub claim must be preserved")
				assert.Equal(t, "org456", claims["org_id"], "org_id claim must be preserved")
			},
		},
		{
			name: "marshals_claims_without_tsid_unchanged",
			identity: &Identity{
				PrincipalInfo: PrincipalInfo{
					Subject: "user123",
					Claims: map[string]any{
						"sub":    "user123",
						"org_id": "org456",
					},
				},
			},
			wantErr: false,
			checkFunc: func(t *testing.T, data []byte) {
				t.Helper()

				var result map[string]any
				require.NoError(t, json.Unmarshal(data, &result))

				claims, ok := result["claims"].(map[string]any)
				require.True(t, ok, "claims should be a map")
				assert.Equal(t, "user123", claims["sub"], "sub must pass through verbatim")
				assert.Equal(t, "org456", claims["org_id"], "org_id must pass through verbatim")
				assert.Len(t, claims, 2, "no claims should be added or dropped when tsid is absent")
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
