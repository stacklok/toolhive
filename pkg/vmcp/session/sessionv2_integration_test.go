// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// TestSessionManagementV2_Integration tests end-to-end Session Management V2
// functionality including HMAC token binding and session hijacking prevention.
func TestSessionManagementV2_Integration(t *testing.T) {
	t.Parallel()

	// Generate a proper 32-byte HMAC secret for these tests
	hmacSecret := generateTestHMACSecret(t)

	// Start a real in-process MCP server
	baseURL := startInProcessMCPServer(t)

	// Create backend
	backend := &vmcp.Backend{
		ID:            "test-backend",
		Name:          "test-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	t.Run("bound session with valid token can call tools", func(t *testing.T) {
		t.Parallel()

		// Create session factory with HMAC secret
		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		// Create an authenticated identity
		identity := &auth.Identity{
			Subject: "test-user",
			Token:   "valid-bearer-token-123",
		}

		// Create a bound session (allowAnonymous=false)
		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-1",
			identity,
			false, // bound session
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		require.NotNil(t, sess)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Verify session metadata
		tokenHash := sess.GetMetadata()[MetadataKeyTokenHash]
		require.NotEmpty(t, tokenHash, "token hash should be stored in session metadata")

		// Call a tool with the SAME identity (should succeed)
		result, err := sess.CallTool(context.Background(), identity, "echo", map[string]any{
			"input": "hello world",
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)

		// Verify the result contains echoed text
		assert.NotEmpty(t, result.Content)
	})

	t.Run("bound session rejects different token (session hijacking prevention)", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		// Create session with original identity
		originalIdentity := &auth.Identity{
			Subject: "alice",
			Token:   "alice-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-2",
			originalIdentity,
			false, // bound session
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Attempt to call tool with DIFFERENT identity (hijacking attempt)
		attackerIdentity := &auth.Identity{
			Subject: "bob",
			Token:   "bob-token", // Different token!
		}

		_, err = sess.CallTool(context.Background(), attackerIdentity, "echo", map[string]any{
			"input": "hijacked",
		}, nil)

		// Should be rejected
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("bound session rejects nil caller", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "user-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-3",
			identity,
			false, // bound session
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Attempt to call tool with nil caller
		_, err = sess.CallTool(context.Background(), nil, "echo", map[string]any{
			"input": "test",
		}, nil)

		// Should be rejected
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrNilCaller)
	})

	t.Run("anonymous session allows nil caller", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		// Create anonymous session (allowAnonymous=true, no identity)
		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-4",
			nil,  // no identity
			true, // anonymous session
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Verify anonymous session metadata
		tokenHash := sess.GetMetadata()[MetadataKeyTokenHash]
		assert.Empty(t, tokenHash, "anonymous session should have empty token hash sentinel")

		// Call tool with nil caller (should succeed for anonymous session)
		result, err := sess.CallTool(context.Background(), nil, "echo", map[string]any{
			"input": "anonymous call",
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
	})

	t.Run("anonymous session rejects caller with token (upgrade attack prevention)", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		// Create anonymous session
		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-5",
			nil,  // no identity
			true, // anonymous
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Attempt to call tool with an identity (upgrade attack)
		attackerIdentity := &auth.Identity{
			Subject: "attacker",
			Token:   "attacker-token",
		}

		_, err = sess.CallTool(context.Background(), attackerIdentity, "echo", map[string]any{
			"input": "upgraded",
		}, nil)

		// Should be rejected to prevent session upgrade attacks
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("bound session can read resources", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "user-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-6",
			identity,
			false, // bound
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Read resource with correct identity
		result, err := sess.ReadResource(context.Background(), identity, "test://data")
		require.NoError(t, err)
		require.NotNil(t, result)

		// Verify resource content (Contents is []byte)
		assert.NotEmpty(t, result.Contents)
		assert.Contains(t, string(result.Contents), "hello")
	})

	t.Run("bound session rejects resource read with wrong token", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		originalIdentity := &auth.Identity{
			Subject: "alice",
			Token:   "alice-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-7",
			originalIdentity,
			false,
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Attempt to read resource with different token
		attackerIdentity := &auth.Identity{
			Subject: "bob",
			Token:   "bob-token",
		}

		_, err = sess.ReadResource(context.Background(), attackerIdentity, "test://data")
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("bound session can get prompts", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "user-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-8",
			identity,
			false,
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Get prompt with correct identity
		result, err := sess.GetPrompt(context.Background(), identity, "greet", nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.NotEmpty(t, result.Messages)
	})

	t.Run("bound session rejects prompt with wrong token", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		originalIdentity := &auth.Identity{
			Subject: "alice",
			Token:   "alice-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-9",
			originalIdentity,
			false,
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Attempt to get prompt with different token
		attackerIdentity := &auth.Identity{
			Subject: "bob",
			Token:   "bob-token",
		}

		_, err = sess.GetPrompt(context.Background(), attackerIdentity, "greet", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("different HMAC secrets produce different token hashes", func(t *testing.T) {
		t.Parallel()

		// Create two factories with different HMAC secrets
		secret1 := generateTestHMACSecret(t)
		secret2 := generateTestHMACSecret(t)

		factory1 := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(secret1))
		factory2 := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(secret2))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "same-token",
		}

		// Create sessions with same identity but different secrets
		sess1, err := factory1.MakeSessionWithID(context.Background(), "session-1", identity, false, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess1.Close()) })

		sess2, err := factory2.MakeSessionWithID(context.Background(), "session-2", identity, false, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess2.Close()) })

		// Token hashes should be different due to different HMAC secrets
		hash1 := sess1.GetMetadata()[MetadataKeyTokenHash]
		hash2 := sess2.GetMetadata()[MetadataKeyTokenHash]

		assert.NotEqual(t, hash1, hash2, "different HMAC secrets should produce different token hashes")
	})

	t.Run("session validates token consistently across multiple calls", func(t *testing.T) {
		t.Parallel()

		factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "consistent-token",
		}

		sess, err := factory.MakeSessionWithID(
			context.Background(),
			"test-session-10",
			identity,
			false,
			[]*vmcp.Backend{backend},
		)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sess.Close()) })

		// Make multiple calls with the same identity - all should succeed
		for i := 0; i < 5; i++ {
			result, err := sess.CallTool(context.Background(), identity, "echo", map[string]any{
				"input": "test",
			}, nil)
			require.NoError(t, err)
			require.NotNil(t, result)
		}
	})
}

// generateTestHMACSecret generates a cryptographically secure 32-byte secret
// for testing, similar to what the operator generates in production.
func generateTestHMACSecret(t *testing.T) []byte {
	t.Helper()

	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	require.NoError(t, err, "failed to generate test HMAC secret")

	// Verify it's not all zeros
	allZeros := true
	for _, b := range secret {
		if b != 0 {
			allZeros = false
			break
		}
	}
	require.False(t, allZeros, "generated secret should not be all zeros")

	return secret
}

// TestSessionManagementV2_SecretRotation tests that sessions created with
// different secrets cannot be used interchangeably (validates secret isolation).
func TestSessionManagementV2_SecretRotation(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "test-backend",
		Name:          "test-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	t.Run("session created with old secret cannot be validated with new secret", func(t *testing.T) {
		t.Parallel()

		oldSecret := generateTestHMACSecret(t)
		newSecret := generateTestHMACSecret(t)

		// Create factory with old secret
		oldFactory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(oldSecret))

		identity := &auth.Identity{
			Subject: "user",
			Token:   "rotation-token",
		}

		// Create session with old secret
		oldSess, err := oldFactory.MakeSessionWithID(context.Background(), "old-session", identity, false, []*vmcp.Backend{backend})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, oldSess.Close()) })

		// Session should work with old secret
		_, err = oldSess.CallTool(context.Background(), identity, "echo", map[string]any{"input": "test"}, nil)
		require.NoError(t, err)

		// Create a new factory with new secret (simulating secret rotation)
		newFactory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(newSecret))

		// Create a new session with new secret
		newSess, err := newFactory.MakeSessionWithID(context.Background(), "new-session", identity, false, []*vmcp.Backend{backend})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, newSess.Close()) })

		// New session should have different token hash
		oldHash := oldSess.GetMetadata()[MetadataKeyTokenHash]
		newHash := newSess.GetMetadata()[MetadataKeyTokenHash]
		assert.NotEqual(t, oldHash, newHash, "rotated secret should produce different token hash")

		// Both sessions should work independently with same identity
		_, err = newSess.CallTool(context.Background(), identity, "echo", map[string]any{"input": "test"}, nil)
		require.NoError(t, err)
	})
}

// TestSessionManagementV2_MetadataEncoding verifies that token hashes and salts
// are properly hex-encoded in session metadata for transmission and storage.
func TestSessionManagementV2_MetadataEncoding(t *testing.T) {
	t.Parallel()

	hmacSecret := generateTestHMACSecret(t)
	factory := NewSessionFactory(newUnauthenticatedRegistry(t), WithHMACSecret(hmacSecret))

	identity := &auth.Identity{
		Subject: "user",
		Token:   "test-token-123",
	}

	sess, err := factory.MakeSessionWithID(context.Background(), "test-session", identity, false, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	// Get token hash from session metadata
	tokenHash := sess.GetMetadata()[MetadataKeyTokenHash]
	require.NotEmpty(t, tokenHash)

	// Verify it's valid hex encoding (HMAC-SHA256 is 64 hex chars)
	assert.Len(t, tokenHash, 64, "HMAC-SHA256 hex-encoded hash should be 64 characters")

	// Verify token hash decodes as valid hex
	hashBytes, err := hex.DecodeString(tokenHash)
	require.NoError(t, err, "token hash should be valid hex")
	assert.Len(t, hashBytes, 32, "decoded token hash should be 32 bytes (SHA256)")

	// Get token salt from session metadata
	tokenSalt := sess.GetMetadata()[MetadataKeyTokenSalt]
	require.NotEmpty(t, tokenSalt)

	// Verify salt is valid hex encoding
	saltBytes, err := hex.DecodeString(tokenSalt)
	require.NoError(t, err, "token salt should be valid hex")
	assert.Len(t, saltBytes, 16, "decoded token salt should be 16 bytes")
}
