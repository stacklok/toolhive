package auth

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentity_String(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		identity    *Identity
		contains    []string
		notContains []string
	}{
		{
			name: "redacts_token",
			identity: &Identity{
				Subject:   "user@example.com",
				Name:      "Test User",
				Email:     "user@example.com",
				Groups:    []string{"admin", "users"},
				Token:     "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.sensitive-token-data",
				TokenType: "Bearer",
			},
			contains: []string{
				"Subject:\"user@example.com\"",
				"Name:\"Test User\"",
				"Token:REDACTED",
				"TokenType:\"Bearer\"",
			},
			notContains: []string{
				"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
				"sensitive-token-data",
			},
		},
		{
			name: "shows_empty_token",
			identity: &Identity{
				Subject: "user@example.com",
				Token:   "",
			},
			contains:    []string{"Token:<empty>"},
			notContains: []string{"REDACTED"},
		},
		{
			name:        "handles_nil_identity",
			identity:    nil,
			contains:    []string{"<nil>"},
			notContains: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := tc.identity.String()

			for _, expected := range tc.contains {
				assert.Contains(t, result, expected)
			}

			for _, forbidden := range tc.notContains {
				assert.NotContains(t, result, forbidden)
			}
		})
	}
}

func TestIdentity_MarshalJSON(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		identity    *Identity
		contains    []string
		notContains []string
	}{
		{
			name: "redacts_token",
			identity: &Identity{
				Subject:   "user@example.com",
				Name:      "Test User",
				Email:     "user@example.com",
				Groups:    []string{"admin", "users"},
				Token:     "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.sensitive-token-data",
				TokenType: "Bearer",
			},
			contains: []string{
				`"subject":"user@example.com"`,
				`"name":"Test User"`,
				`"email":"user@example.com"`,
				`"token":"REDACTED"`,
				`"tokenType":"Bearer"`,
			},
			notContains: []string{
				"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
				"sensitive-token-data",
			},
		},
		{
			name: "preserves_empty_token",
			identity: &Identity{
				Subject: "user@example.com",
				Token:   "",
			},
			contains:    []string{`"subject":"user@example.com"`},
			notContains: []string{`"token":"REDACTED"`},
		},
		{
			name:        "handles_nil_identity",
			identity:    nil,
			contains:    []string{"null"},
			notContains: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(tc.identity)
			require.NoError(t, err)

			result := string(data)

			for _, expected := range tc.contains {
				assert.Contains(t, result, expected)
			}

			for _, forbidden := range tc.notContains {
				assert.NotContains(t, result, forbidden)
			}
		})
	}
}
