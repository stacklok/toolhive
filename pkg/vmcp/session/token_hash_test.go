// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
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
			name:  "non-empty token returns SHA256 hex",
			token: "my-bearer-token",
			want: func() string {
				h := sha256.Sum256([]byte("my-bearer-token"))
				return hex.EncodeToString(h[:])
			}(),
		},
		{
			name:  "different tokens produce different hashes",
			token: "another-token",
			want: func() string {
				h := sha256.Sum256([]byte("another-token"))
				return hex.EncodeToString(h[:])
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, HashToken(tt.token))
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeTokenHash
// ---------------------------------------------------------------------------

func TestComputeTokenHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *auth.Identity
		want     string // empty string means anonymous sentinel
	}{
		{
			name:     "nil identity returns anonymous sentinel",
			identity: nil,
			want:     "",
		},
		{
			name:     "identity with empty token returns anonymous sentinel",
			identity: &auth.Identity{Subject: "user1", Token: ""},
			want:     "",
		},
		{
			name:     "identity with token returns SHA256 hex",
			identity: &auth.Identity{Subject: "user1", Token: "my-bearer-token"},
			want: func() string {
				h := sha256.Sum256([]byte("my-bearer-token"))
				return hex.EncodeToString(h[:])
			}(),
		},
		{
			name:     "different tokens produce different hashes",
			identity: &auth.Identity{Subject: "user2", Token: "another-token"},
			want: func() string {
				h := sha256.Sum256([]byte("another-token"))
				return hex.EncodeToString(h[:])
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ComputeTokenHash(tt.identity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestComputeTokenHash_RawTokenNotStored(t *testing.T) {
	t.Parallel()

	const rawToken = "super-secret-bearer-token"
	identity := &auth.Identity{Subject: "user1", Token: rawToken}
	hash := ComputeTokenHash(identity)

	// Hash must not equal the raw token.
	assert.NotEqual(t, rawToken, hash, "raw token must not be returned as the hash")
	// Hash must be non-empty for an authenticated identity.
	assert.NotEmpty(t, hash)
}

func TestComputeTokenHash_DeterministicForSameToken(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{Subject: "user", Token: "deterministic-token"}
	h1 := ComputeTokenHash(identity)
	h2 := ComputeTokenHash(identity)
	assert.Equal(t, h1, h2, "same token must always produce the same hash")
}

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

	t.Run("authenticated session stores SHA256 hash", func(t *testing.T) {
		t.Parallel()

		const rawToken = "test-bearer-token"
		identity := &auth.Identity{Subject: "alice", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), identity, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set")

		expectedHash := ComputeTokenHash(identity)
		assert.Equal(t, expectedHash, storedHash)
		// Raw token must never appear in metadata.
		assert.NotEqual(t, rawToken, storedHash)
	})

	t.Run("anonymous session stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), nil, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set even for anonymous sessions")
		assert.Empty(t, storedHash, "anonymous session must store empty sentinel")
	})

	t.Run("identity with empty token stores empty sentinel", func(t *testing.T) {
		t.Parallel()

		identity := &auth.Identity{Subject: "user", Token: ""}
		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSession(t.Context(), identity, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash := sess.GetMetadata()[MetadataKeyTokenHash]
		assert.Empty(t, storedHash, "empty-token identity must store empty sentinel")
	})

	t.Run("MakeSessionWithID also stores token hash", func(t *testing.T) {
		t.Parallel()

		const rawToken = "id-specific-token"
		identity := &auth.Identity{Subject: "bob", Token: rawToken}

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), "explicit-session-id", identity, nil)
		require.NoError(t, err)
		require.NotNil(t, sess)

		storedHash, present := sess.GetMetadata()[MetadataKeyTokenHash]
		require.True(t, present, "MetadataKeyTokenHash must be set")
		assert.Equal(t, ComputeTokenHash(identity), storedHash)
	})
}
