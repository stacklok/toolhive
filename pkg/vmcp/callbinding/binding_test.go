// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
)

const testHash = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"

func testEntry() map[string]any {
	return map[string]any{
		"type":             toolcalldetails.Type,
		"server":           "github",
		"tool":             "github_read",
		"arguments_sha256": testHash,
		"operation_id":     "op-1",
	}
}

func testClaims(details any) map[string]any {
	claims := map[string]any{
		"iss":       "https://as.example.com",
		"sub":       "user",
		"client_id": "mecatl",
		"jti":       "jti-1",
		"exp":       float64(1_800_000_000),
	}
	if details != nil {
		claims["authorization_details"] = details
	}
	return claims
}

func withClaim(claims map[string]any, name string, value any) map[string]any {
	if value == nil {
		delete(claims, name)
	} else {
		claims[name] = value
	}
	return claims
}

// nullDetailsClaims has an authorization_details claim that was JSON null.
func nullDetailsClaims() map[string]any {
	claims := testClaims(nil)
	claims["authorization_details"] = nil
	return claims
}

func TestFromClaims(t *testing.T) {
	t.Parallel()

	listed := map[string]struct{}{"mecatl": {}}
	otherType := map[string]any{"type": "https://example.com/other", "anything": []any{1.0}}
	withUnknown := testEntry()
	withUnknown["extra"] = "x"
	listEntry := map[string]any{"type": toolcalldetails.Type, "server": "github", "method": "tools/list"}

	tests := []struct {
		name     string
		claims   map[string]any
		required map[string]struct{}
		reason   string
		bound    bool
	}{
		{name: "unlisted client without details passes", claims: testClaims(nil)},
		{name: "unlisted client with only another type passes", claims: testClaims([]any{otherType})},
		{name: "unlisted client with empty details passes", claims: testClaims([]any{})},
		{name: "listed client without details", claims: testClaims(nil), required: listed, reason: ReasonEntryRequired},
		{name: "listed client with only another type", claims: testClaims([]any{otherType}), required: listed,
			reason: ReasonEntryRequired},
		{name: "listed list includes no empty client", claims: withClaim(testClaims(nil), "client_id", nil),
			required: map[string]struct{}{"": {}}},
		{name: "unlisted client with an entry is bound", claims: testClaims([]any{testEntry()}), bound: true},
		{name: "listed client with an entry is bound", claims: testClaims([]any{otherType, testEntry()}),
			required: listed, bound: true},
		{name: "list-call entry is bound", claims: testClaims([]any{listEntry}), bound: true},
		{name: "two entries", claims: testClaims([]any{testEntry(), testEntry()}), reason: ReasonSingleEntry},
		{name: "unknown member", claims: testClaims([]any{withUnknown}), reason: ReasonMalformedEntry},
		{name: "details not an array", claims: testClaims(map[string]any{"type": toolcalldetails.Type}),
			reason: ReasonMalformedEntry},
		{name: "details null", claims: nullDetailsClaims(), reason: ReasonMalformedEntry},
		{name: "element not an object", claims: testClaims([]any{"x"}), reason: ReasonMalformedEntry},
		{name: "element without a type", claims: testClaims([]any{map[string]any{"server": "github"}}),
			reason: ReasonMalformedEntry},
		{name: "element with a non-string type", claims: testClaims([]any{map[string]any{"type": 1.0}}),
			reason: ReasonMalformedEntry},
		{name: "entry without jti", claims: withClaim(testClaims([]any{testEntry()}), "jti", nil),
			reason: ReasonClaimsRequired},
		{name: "entry with empty jti", claims: withClaim(testClaims([]any{testEntry()}), "jti", ""),
			reason: ReasonClaimsRequired},
		{name: "entry without exp", claims: withClaim(testClaims([]any{testEntry()}), "exp", nil),
			reason: ReasonClaimsRequired},
		{name: "entry with a string exp", claims: withClaim(testClaims([]any{testEntry()}), "exp", "1800000000"),
			reason: ReasonClaimsRequired},
		{name: "entry with json.Number exp", claims: withClaim(testClaims([]any{testEntry()}), "exp",
			json.Number("1800000000")), bound: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token, reason := FromClaims(tc.claims, tc.required)
			assert.Equal(t, tc.reason, reason)
			if !tc.bound {
				assert.Nil(t, token)
				return
			}
			require.NotNil(t, token)
			assert.Equal(t, "github", token.Entry.Server)
			assert.Equal(t, "https://as.example.com\x00jti-1", token.Key)
			assert.Equal(t, "jti-1", token.JTI)
			assert.Equal(t, "mecatl", token.ClientID)
			assert.Equal(t, time.Unix(1_800_000_000, 0), token.ExpiresAt)
		})
	}
}

func TestFromClaimsEntryFields(t *testing.T) {
	t.Parallel()
	token, reason := FromClaims(testClaims([]any{testEntry()}), nil)
	require.Empty(t, reason)
	assert.Equal(t, toolcalldetails.Entry{
		Server: "github", Tool: "github_read", ArgumentsSHA256: testHash, OperationID: "op-1",
	}, token.Entry)
}
