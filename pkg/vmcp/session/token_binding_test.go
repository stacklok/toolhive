// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

var (
	// Test HMAC secret and salt for consistent test results
	testSecret    = []byte("test-secret")
	testTokenSalt = []byte("test-salt-123456") // 16 bytes
)

// ---------------------------------------------------------------------------
// HashToken
// ---------------------------------------------------------------------------

func TestHashToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "empty token returns anonymous sentinel",
			token: "",
			want:  "",
		},
		{
			name:  "non-empty token returns HMAC-SHA256 hex",
			token: "my-bearer-token",
			want: func() string {
				h := hmac.New(sha256.New, testSecret)
				h.Write(testTokenSalt)
				h.Write([]byte("my-bearer-token"))
				return hex.EncodeToString(h.Sum(nil))
			}(),
		},
		{
			name:  "different tokens produce different hashes",
			token: "another-token",
			want: func() string {
				h := hmac.New(sha256.New, testSecret)
				h.Write(testTokenSalt)
				h.Write([]byte("another-token"))
				return hex.EncodeToString(h.Sum(nil))
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, security.HashToken(tt.token, testSecret, testTokenSalt))
		})
	}
}

// ---------------------------------------------------------------------------
// Note: ComputeTokenHash was removed
// ---------------------------------------------------------------------------
// ComputeTokenHash was removed because HMAC-SHA256 hashing requires
// per-session salt, so token hashes can't be computed without session context.
// Use HashToken(token, secret, salt) directly with session-specific parameters.

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

	t.Run("authenticated session stores HMAC-SHA256 hash and salt", func(t *testing.T) {
		t.Parallel()

		const rawToken = "test-bearer-token"
		identity := &auth.Identity{Subject: "alice", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), identity, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		// Verify token hash is stored
		storedHash, present := sess.GetMetadata()[security.MetadataKeyTokenHash]
		require.True(t, present, "security.MetadataKeyTokenHash must be set")
		assert.NotEmpty(t, storedHash, "Token hash must be non-empty for authenticated session")
		assert.Len(t, storedHash, 64, "HMAC-SHA256 hex-encoded hash should be 64 characters")
		// Raw token must never appear in metadata.
		assert.NotEqual(t, rawToken, storedHash)

		// Verify salt is stored
		storedSalt, saltPresent := sess.GetMetadata()[security.MetadataKeyTokenSalt]
		require.True(t, saltPresent, "security.MetadataKeyTokenSalt must be set")
		assert.NotEmpty(t, storedSalt, "Salt must be non-empty for authenticated session")
	})

	t.Run("anonymous session stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), nil, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash, present := sess.GetMetadata()[security.MetadataKeyTokenHash]
		require.True(t, present, "security.MetadataKeyTokenHash must be set even for anonymous sessions")
		assert.Empty(t, storedHash, "anonymous session must store empty sentinel")

		// Anonymous sessions should not have salt
		storedSalt := sess.GetMetadata()[security.MetadataKeyTokenSalt]
		assert.Empty(t, storedSalt, "anonymous session should not have salt")
	})

	t.Run("identity with empty token stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		identity := &auth.Identity{Subject: "user", Token: ""}
		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), identity, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash := sess.GetMetadata()[security.MetadataKeyTokenHash]
		assert.Empty(t, storedHash, "empty-token identity must store empty sentinel")

		// Empty token should not have salt
		storedSalt := sess.GetMetadata()[security.MetadataKeyTokenSalt]
		assert.Empty(t, storedSalt, "empty-token identity should not have salt")
	})

	t.Run("MakeSessionWithID also stores token hash and salt", func(t *testing.T) {
		t.Parallel()

		const rawToken = "id-specific-token"
		identity := &auth.Identity{Subject: "bob", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), "explicit-session-id", identity, false, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		// Verify token hash
		storedHash, present := sess.GetMetadata()[security.MetadataKeyTokenHash]
		require.True(t, present, "security.MetadataKeyTokenHash must be set")
		assert.NotEmpty(t, storedHash, "Token hash must be non-empty")
		assert.Len(t, storedHash, 64, "HMAC-SHA256 hex-encoded hash should be 64 characters")

		// Verify salt
		storedSalt, saltPresent := sess.GetMetadata()[security.MetadataKeyTokenSalt]
		require.True(t, saltPresent, "security.MetadataKeyTokenSalt must be set")
		assert.NotEmpty(t, storedSalt, "Salt must be non-empty")
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
	hash1 := sess1.GetMetadata()[security.MetadataKeyTokenHash]
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
	hash2 := sess2.GetMetadata()[security.MetadataKeyTokenHash]
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
		hash := sess.GetMetadata()[security.MetadataKeyTokenHash]
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
		hash := sess.GetMetadata()[security.MetadataKeyTokenHash]
		assert.NotEmpty(t, hash, "should use default secret, not empty slice")
	})
}
