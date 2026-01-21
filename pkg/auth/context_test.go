// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentityContext_StoreAndRetrieve verifies basic context storage and retrieval functionality.
func TestIdentityContext_StoreAndRetrieve(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a test identity
	identity := &Identity{
		Subject: "user123",
		Name:    "Alice Smith",
		Email:   "alice@example.com",
		Groups:  []string{"admins", "developers"},
		Claims: map[string]any{
			"org_id": "org456",
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

	// Retrieval should detect the nil pointer
	identity, ok := IdentityFromContext(ctx)
	assert.True(t, ok, "expected value to be present")
	assert.Nil(t, identity, "expected nil identity")
}

// TestIdentityContext_Overwrite verifies that storing a new identity replaces the old one.
func TestIdentityContext_Overwrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Store first identity
	identity1 := &Identity{Subject: "user1"}
	ctx = WithIdentity(ctx, identity1)

	// Store second identity (overwrites first)
	identity2 := &Identity{Subject: "user2"}
	ctx = WithIdentity(ctx, identity2)

	// Retrieve identity
	retrieved, ok := IdentityFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "user2", retrieved.Subject)
}
