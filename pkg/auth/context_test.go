// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentityContext_StoreAndRetrieve verifies basic context storage and retrieval functionality.
func TestIdentityContext_StoreAndRetrieve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a test identity
	identity := &Identity{
		PrincipalInfo: PrincipalInfo{
			Subject: "user123",
			Name:    "Alice Smith",
			Email:   "alice@example.com",
			Groups:  []string{"admins", "developers"},
			Claims: map[string]any{
				"org_id": "org456",
			},
		},
		Token:     "test-token",
		TokenType: "Bearer",
		Metadata: map[string]string{
			"source": "test",
		},
	}

	// Store identity in context
	ctx = WithIdentity(ctx, identity)

	// Retrieve identity from context
	retrieved, ok := IdentityFromContext(ctx)
	require.True(t, ok, "expected identity to be present in context")

	// Verify all fields match
	assert.Equal(t, identity.Subject, retrieved.Subject)
	assert.Equal(t, identity.Name, retrieved.Name)
	assert.Equal(t, identity.Email, retrieved.Email)
	assert.Equal(t, len(identity.Groups), len(retrieved.Groups))
	for i, group := range identity.Groups {
		assert.Equal(t, group, retrieved.Groups[i])
	}
	assert.Equal(t, identity.Claims["org_id"], retrieved.Claims["org_id"])
	assert.Equal(t, identity.Token, retrieved.Token)
	assert.Equal(t, identity.TokenType, retrieved.TokenType)
	assert.Equal(t, identity.Metadata["source"], retrieved.Metadata["source"])
}

// TestIdentityContext_NilIdentity verifies that storing nil doesn't change the context.
func TestIdentityContext_NilIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Store nil identity
	newCtx := WithIdentity(ctx, nil)

	// Context should remain unchanged
	assert.Equal(t, ctx, newCtx)

	// Retrieval should fail
	_, ok := IdentityFromContext(newCtx)
	assert.False(t, ok, "expected no identity in context")
}

// TestIdentityContext_MissingIdentity verifies retrieval when identity not present.
func TestIdentityContext_MissingIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Attempt to retrieve non-existent identity
	identity, ok := IdentityFromContext(ctx)
	assert.False(t, ok, "expected identity to be absent")
	assert.Nil(t, identity)
}

// TestIdentityContext_ExplicitNilValue tests edge case of explicitly stored nil Identity.
func TestIdentityContext_ExplicitNilValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Explicitly store nil Identity pointer in context (edge case)
	ctx = context.WithValue(ctx, IdentityContextKey{}, (*Identity)(nil))

	// Typed-nil pointers must read as absent so fallback identity injection can proceed.
	identity, ok := IdentityFromContext(ctx)
	assert.False(t, ok, "typed-nil identity should not be treated as present")
	assert.Nil(t, identity, "expected nil identity")
}

// TestIdentityContext_Overwrite verifies that storing a new identity replaces the old one.
func TestIdentityContext_Overwrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Store first identity
	identity1 := &Identity{PrincipalInfo: PrincipalInfo{Subject: "user1"}}
	ctx = WithIdentity(ctx, identity1)

	// Store second identity (overwrites first)
	identity2 := &Identity{PrincipalInfo: PrincipalInfo{Subject: "user2"}}
	ctx = WithIdentity(ctx, identity2)

	// Retrieve identity
	retrieved, ok := IdentityFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "user2", retrieved.Subject)
}

// TestClaimsToIdentity_PopulatesPlatformUserID verifies that claimsToIdentity fills
// PlatformUserID from the `sub` claim, giving storage layers that key on the canonical
// platform user a single, documented place to read it.
func TestClaimsToIdentity_PopulatesPlatformUserID(t *testing.T) {
	t.Parallel()
	id, err := claimsToIdentity(jwt.MapClaims{"sub": "user123"}, "tok")
	require.NoError(t, err)
	assert.Equal(t, "user123", id.PlatformUserID)
}

// TestPlatformUserContext_StoreAndRetrieve verifies the dedicated platform-user key
// round-trips and that an empty userID leaves the context (and the identity key)
// untouched.
func TestPlatformUserContext_StoreAndRetrieve(t *testing.T) {
	t.Parallel()

	ctx := WithPlatformUser(context.Background(), "user-1")
	uid, ok := PlatformUserFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "user-1", uid)

	// Empty userID is a no-op: context unchanged, nothing stored.
	base := context.Background()
	unchanged := WithPlatformUser(base, "")
	assert.Equal(t, base, unchanged)
	_, ok = PlatformUserFromContext(unchanged)
	assert.False(t, ok)

	// The platform-user key must NOT satisfy IdentityFromContext — that is the
	// whole point of keeping the two structurally separate.
	_, hasIdentity := IdentityFromContext(ctx)
	assert.False(t, hasIdentity, "platform-user key must not read as an authenticated Identity")
}

// TestCanonicalUserFromContext verifies the precedence storage relies on: the
// dedicated platform-user key wins, the authenticated Identity's PlatformUserID is
// the fallback, and an empty PlatformUserID does not count as present.
func TestCanonicalUserFromContext(t *testing.T) {
	t.Parallel()

	withIdentity := func(sub, platformUserID string) context.Context {
		return WithIdentity(context.Background(), &Identity{
			PrincipalInfo: PrincipalInfo{Subject: sub, PlatformUserID: platformUserID},
		})
	}

	tests := []struct {
		name   string
		ctx    context.Context
		wantID string
		wantOK bool
	}{
		{
			name:   "dedicated key only",
			ctx:    WithPlatformUser(context.Background(), "key-user"),
			wantID: "key-user",
			wantOK: true,
		},
		{
			name:   "identity fallback",
			ctx:    withIdentity("sub-1", "identity-user"),
			wantID: "identity-user",
			wantOK: true,
		},
		{
			name:   "dedicated key wins over identity",
			ctx:    WithPlatformUser(withIdentity("sub-1", "identity-user"), "key-user"),
			wantID: "key-user",
			wantOK: true,
		},
		{
			name:   "identity with empty platform user id is not present",
			ctx:    withIdentity("sub-1", ""),
			wantID: "",
			wantOK: false,
		},
		{
			name:   "neither set",
			ctx:    context.Background(),
			wantID: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			uid, ok := CanonicalUserFromContext(tt.ctx)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantID, uid)
		})
	}
}
