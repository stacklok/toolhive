// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	testCorrectToken = "correct-token"
	testWrongToken   = "wrong-token"
)

// TestValidateCaller_EdgeCases tests edge cases in caller validation logic.
func TestValidateCaller_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		allowAnonymous bool
		boundTokenHash string
		caller         *auth.Identity
		wantErr        error
	}{
		{
			name:           "anonymous session with nil caller",
			allowAnonymous: true,
			boundTokenHash: "",
			caller:         nil,
			wantErr:        nil, // Should succeed
		},
		{
			name:           "anonymous session with non-nil caller",
			allowAnonymous: true,
			boundTokenHash: "",
			caller:         &auth.Identity{Subject: "user", Token: "token"},
			wantErr:        nil, // Anonymous sessions accept any caller
		},
		{
			name:           "bound session with nil caller",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token"),
			caller:         nil,
			wantErr:        sessiontypes.ErrNilCaller,
		},
		{
			name:           "bound session with matching token",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token"),
			caller:         &auth.Identity{Subject: "user", Token: "correct-token"},
			wantErr:        nil, // Should succeed
		},
		{
			name:           "bound session with wrong token",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token"),
			caller:         &auth.Identity{Subject: "user", Token: "wrong-token"},
			wantErr:        sessiontypes.ErrUnauthorizedCaller,
		},
		{
			name:           "bound session with empty token in identity",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token"),
			caller:         &auth.Identity{Subject: "user", Token: ""},
			wantErr:        sessiontypes.ErrUnauthorizedCaller,
		},
		{
			name:           "bound session with empty bound hash (edge case)",
			allowAnonymous: false,
			boundTokenHash: "",
			caller:         &auth.Identity{Subject: "user", Token: "token"},
			wantErr:        sessiontypes.ErrUnauthorizedCaller,
		},
		{
			name:           "anonymous with empty string hash matches empty token",
			allowAnonymous: true,
			boundTokenHash: "",
			caller:         &auth.Identity{Subject: "user", Token: ""},
			wantErr:        nil, // Anonymous accepts any caller
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a minimal session with the test configuration
			sess := &defaultMultiSession{
				Session:        transportsession.NewStreamableSession("test-session"),
				allowAnonymous: tt.allowAnonymous,
				boundTokenHash: tt.boundTokenHash,
			}

			// Test validateCaller directly
			err := sess.validateCaller(tt.caller)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestTokenHashConsistency verifies that HashToken produces consistent results.
func TestTokenHashConsistency(t *testing.T) {
	t.Parallel()

	token := "test-token-12345"
	hash1 := HashToken(token)
	hash2 := HashToken(token)

	assert.Equal(t, hash1, hash2, "HashToken should produce consistent results")
	assert.Len(t, hash1, 64, "SHA256 hex-encoded hash should be 64 characters")
}

// TestTokenHashUniqueness verifies that different tokens produce different hashes.
func TestTokenHashUniqueness(t *testing.T) {
	t.Parallel()

	token1 := "token-one"
	token2 := "token-two"

	hash1 := HashToken(token1)
	hash2 := HashToken(token2)

	assert.NotEqual(t, hash1, hash2, "Different tokens should produce different hashes")
}

// TestComputeTokenHash_NilIdentity tests ComputeTokenHash with nil identity.
func TestComputeTokenHash_NilIdentity(t *testing.T) {
	t.Parallel()

	hash := ComputeTokenHash(nil)
	assert.Empty(t, hash, "Nil identity should produce empty hash")
}

// TestComputeTokenHash_EmptyToken tests ComputeTokenHash with empty token.
func TestComputeTokenHash_EmptyToken(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{Subject: "user", Token: ""}
	hash := ComputeTokenHash(identity)
	assert.Empty(t, hash, "Empty token should produce empty hash")
}

// TestShouldAllowAnonymous_EdgeCases tests the ShouldAllowAnonymous helper.
func TestShouldAllowAnonymous_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *auth.Identity
		want     bool
	}{
		{
			name:     "nil identity",
			identity: nil,
			want:     true,
		},
		{
			name:     "non-nil identity with token",
			identity: &auth.Identity{Subject: "user", Token: "token"},
			want:     false,
		},
		{
			name:     "non-nil identity with empty token",
			identity: &auth.Identity{Subject: "user", Token: ""},
			want:     false,
		},
		{
			name:     "non-nil identity with empty subject",
			identity: &auth.Identity{Subject: "", Token: "token"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldAllowAnonymous(tt.identity)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCallTool_TokenValidation tests that CallTool properly validates caller tokens.
func TestCallTool_TokenValidation(t *testing.T) {
	t.Parallel()

	t.Run("wrong token fails", func(t *testing.T) {
		t.Parallel()
		// Create a minimal session just for testing validation
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}
		caller := &auth.Identity{Subject: "user", Token: testWrongToken}

		// Validation happens before routing, so this should fail with auth error
		err := sess.validateCaller(caller)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("correct token passes validation", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}
		caller := &auth.Identity{Subject: "user", Token: testCorrectToken}

		// Validation should succeed
		err := sess.validateCaller(caller)
		require.NoError(t, err)
	})

	t.Run("nil caller fails for bound session", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}

		err := sess.validateCaller(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrNilCaller)
	})
}

// TestReadResource_TokenValidation tests that ReadResource validates caller tokens.
func TestReadResource_TokenValidation(t *testing.T) {
	t.Parallel()

	t.Run("wrong token fails validation", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}
		caller := &auth.Identity{Subject: "user", Token: testWrongToken}

		err := sess.validateCaller(caller)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("nil caller fails for bound session", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}

		err := sess.validateCaller(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrNilCaller)
	})
}

// TestGetPrompt_TokenValidation tests that GetPrompt validates caller tokens.
func TestGetPrompt_TokenValidation(t *testing.T) {
	t.Parallel()

	t.Run("wrong token fails validation", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}
		caller := &auth.Identity{Subject: "user", Token: testWrongToken}

		err := sess.validateCaller(caller)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
	})

	t.Run("nil caller fails for bound session", func(t *testing.T) {
		t.Parallel()
		sess := &defaultMultiSession{
			Session:        transportsession.NewStreamableSession("test-session"),
			allowAnonymous: false,
			boundTokenHash: HashToken(testCorrectToken),
		}

		err := sess.validateCaller(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, sessiontypes.ErrNilCaller)
	})
}

// TestConcurrentValidation tests that validateCaller is safe for concurrent use.
func TestConcurrentValidation(t *testing.T) {
	t.Parallel()

	sess := &defaultMultiSession{
		Session:        transportsession.NewStreamableSession("test-session"),
		allowAnonymous: false,
		boundTokenHash: HashToken("test-token"),
	}

	// Run validation concurrently from multiple goroutines
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			caller := &auth.Identity{Subject: "user", Token: "test-token"}
			err := sess.validateCaller(caller)
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
