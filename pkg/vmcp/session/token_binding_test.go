// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// ---------------------------------------------------------------------------
// makeSession stores token hash in metadata
// ---------------------------------------------------------------------------

// nilBackendConnector is a connector that returns (nil, nil, nil), causing the
// backend to be skipped during init. This lets us exercise session-metadata
// logic without real backend connections.
func nilBackendConnector() backendConnector {
	return func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		return nil, nil, nil
	}
}

func TestMakeSession_StoresTokenHash(t *testing.T) {
	t.Parallel()

	t.Run("authenticated session stores HMAC-SHA256 hash", func(t *testing.T) {
		t.Parallel()

		const rawToken = "test-bearer-token"
		identity := &auth.Identity{Subject: "alice", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), identity, false, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		// Verify token hash is stored
		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set")
		assert.NotEmpty(t, storedHash, "Token hash must be non-empty for authenticated session")
		assert.Len(t, storedHash, 64, "HMAC-SHA256 hex-encoded hash should be 64 characters")
		// Raw token must never appear in metadata.
		assert.NotEqual(t, rawToken, storedHash)

		// Verify salt is stored for authenticated sessions
		storedSalt, saltPresent := sess.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]
		require.True(t, saltPresent, "MetadataKeyTokenSalt must be set for authenticated sessions")
		assert.NotEmpty(t, storedSalt, "Salt must be non-empty for authenticated session")
	})

	t.Run("anonymous session stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set even for anonymous sessions")
		assert.Empty(t, storedHash, "anonymous session must store empty sentinel")

		// Salt must not be present for anonymous sessions
		storedSalt := sess.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]
		assert.Empty(t, storedSalt, "anonymous session must not store a salt")
	})

	t.Run("identity with empty token stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		identity := &auth.Identity{Subject: "user", Token: ""}
		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), identity, true, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash := sess.GetMetadata()[MetadataKeyTokenHash]
		assert.Empty(t, storedHash, "empty-token identity must store empty sentinel")

		// Salt must not be present for empty-token (anonymous) sessions
		storedSalt := sess.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]
		assert.Empty(t, storedSalt, "empty-token identity must not store a salt")
	})

	t.Run("MakeSessionWithID also stores token hash", func(t *testing.T) {
		t.Parallel()

		const rawToken = "id-specific-token"
		identity := &auth.Identity{Subject: "bob", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), "explicit-session-id", identity, false, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		// Verify token hash
		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set")
		assert.NotEmpty(t, storedHash, "Token hash must be non-empty")
		assert.Len(t, storedHash, 64, "HMAC-SHA256 hex-encoded hash should be 64 characters")

		// Verify salt is stored for authenticated sessions
		storedSalt, saltPresent := sess.GetMetadata()[sessiontypes.MetadataKeyTokenSalt]
		require.True(t, saltPresent, "MetadataKeyTokenSalt must be set for authenticated sessions")
		assert.NotEmpty(t, storedSalt, "Salt must be non-empty for authenticated session")
	})
}

// ---------------------------------------------------------------------------
// MakeSessionWithID validation
// ---------------------------------------------------------------------------

// TestMakeSessionWithID_ValidationOfAllowAnonymous tests that MakeSessionWithID
// validates consistency between identity and allowAnonymous parameters.
func TestMakeSessionWithID_ValidationOfAllowAnonymous(t *testing.T) {
	t.Parallel()

	factory := NewSessionFactory(nil)

	t.Run("rejects anonymous session with bearer token", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{Subject: "user", Token: "bearer-token"}
		_, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session",
			identity,
			true, // allowAnonymous=true but identity has token
			nil,  // no backends needed for validation test
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create anonymous session")
		assert.Contains(t, err.Error(), "with bearer token")
	})

	t.Run("rejects bound session without bearer token (nil identity)", func(t *testing.T) {
		t.Parallel()
		_, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session",
			nil,   // no identity
			false, // allowAnonymous=false but no identity
			nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create bound session")
		assert.Contains(t, err.Error(), "without bearer token")
	})

	t.Run("rejects bound session without bearer token (empty token)", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{Subject: "user", Token: ""} // empty token
		_, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session",
			identity,
			false, // allowAnonymous=false but token is empty
			nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot create bound session")
		assert.Contains(t, err.Error(), "without bearer token")
	})

	t.Run("allows anonymous session with nil identity", func(t *testing.T) {
		t.Parallel()
		_, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session",
			nil,  // no identity
			true, // allowAnonymous=true - consistent
			nil,
		)
		require.NoError(t, err)
	})

	t.Run("allows anonymous session with empty token", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{Subject: "user", Token: ""}
		_, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session",
			identity,
			true, // allowAnonymous=true and token is empty - consistent
			nil,
		)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// WithHMACSecret defensive copy
// ---------------------------------------------------------------------------

// TestWithHMACSecret_DefensiveCopy verifies that WithHMACSecret makes a defensive
// copy of the secret to prevent external modification after assignment.
func TestWithHMACSecret_DefensiveCopy(t *testing.T) {
	t.Parallel()

	// Create a mutable secret
	secretSlice := []byte("original-secret-value")

	// Create factory with the secret
	factory := newSessionFactoryWithConnector(nilBackendConnector(), WithHMACSecret(secretSlice))

	identity := &auth.Identity{Subject: "user", Token: "test-token"}

	// Create first session before modification
	sess1, err := factory.MakeSessionWithID(context.Background(), "session-1", identity, false, nil)
	require.NoError(t, err)

	// Verify first session was created successfully
	hash1 := sess1.GetMetadata()[MetadataKeyTokenHash]
	require.NotEmpty(t, hash1, "first session should have token hash")

	// Maliciously modify the secret slice after passing it to WithHMACSecret
	for i := range secretSlice {
		secretSlice[i] = 0xFF
	}

	// Create second session after modification - should still work correctly
	// because WithHMACSecret made a defensive copy
	sess2, err := factory.MakeSessionWithID(context.Background(), "session-2", identity, false, nil)
	require.NoError(t, err)

	// Verify second session was created successfully
	hash2 := sess2.GetMetadata()[MetadataKeyTokenHash]
	require.NotEmpty(t, hash2, "second session should have token hash")

	// Both sessions should still be able to validate the original token
	// (proving the factory used the original secret, not the modified one).
	// We verify this by calling a session method that requires authentication.
	ctx := context.Background()

	// First session should accept the original token and fail with ErrToolNotFound,
	// not an auth error (which would indicate the secret was corrupted)
	_, err = sess1.CallTool(ctx, identity, "nonexistent-tool", nil, nil)
	assert.ErrorIs(t, err, ErrToolNotFound, "should fail with tool not found error")
	assert.False(t, errors.Is(err, sessiontypes.ErrUnauthorizedCaller),
		"should not be an auth error (would indicate corrupted secret)")

	// Second session should also accept the original token and fail with ErrToolNotFound
	_, err = sess2.CallTool(ctx, identity, "nonexistent-tool", nil, nil)
	assert.ErrorIs(t, err, ErrToolNotFound, "should fail with tool not found error")
	assert.False(t, errors.Is(err, sessiontypes.ErrUnauthorizedCaller),
		"should not be an auth error (would indicate corrupted secret)")
}

// TestWithHMACSecret_RejectsEmptySecret verifies that WithHMACSecret rejects
// nil or empty secrets to prevent silent security downgrades.
func TestWithHMACSecret_RejectsEmptySecret(t *testing.T) {
	t.Parallel()

	t.Run("nil secret is rejected", func(t *testing.T) {
		t.Parallel()

		// Create factory with nil secret (should fall back to default)
		factory := NewSessionFactory(nil, WithHMACSecret(nil))

		identity := &auth.Identity{Subject: "user", Token: "test-token"}
		sess, err := factory.MakeSessionWithID(context.Background(), "test-session", identity, false, nil)
		require.NoError(t, err)

		// Should still create a valid session with default secret
		hash := sess.GetMetadata()[MetadataKeyTokenHash]
		assert.NotEmpty(t, hash, "should use default secret, not nil")
	})

	t.Run("empty secret is rejected", func(t *testing.T) {
		t.Parallel()

		// Create factory with empty secret (should fall back to default)
		factory := NewSessionFactory(nil, WithHMACSecret([]byte{}))

		identity := &auth.Identity{Subject: "user", Token: "test-token"}
		sess, err := factory.MakeSessionWithID(context.Background(), "test-session", identity, false, nil)
		require.NoError(t, err)

		// Should still create a valid session with default secret
		hash := sess.GetMetadata()[MetadataKeyTokenHash]
		assert.NotEmpty(t, hash, "should use default secret, not empty slice")
	})
}
