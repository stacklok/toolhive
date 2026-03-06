// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

var (
	// Test HMAC secret and salt for consistent test results
	testSecret    = []byte("test-secret")
	testTokenSalt = []byte("test-salt-123456") // 16 bytes
)

// mockSession is a minimal implementation of MultiSession for testing.
type mockSession struct {
	metadata map[string]string
}

func newMockSession(_ string) *mockSession {
	return &mockSession{
		metadata: make(map[string]string),
	}
}

func (m *mockSession) SetMetadata(key, value string) {
	m.metadata[key] = value
}

func (m *mockSession) GetMetadata() map[string]string {
	return m.metadata
}

func (*mockSession) CallTool(_ context.Context, _ *auth.Identity, _ string, _ map[string]any, _ map[string]any) (*vmcp.ToolCallResult, error) {
	return nil, nil
}

func (*mockSession) ReadResource(_ context.Context, _ *auth.Identity, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, nil
}

func (*mockSession) GetPrompt(_ context.Context, _ *auth.Identity, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return nil, nil
}

func (*mockSession) Close() error {
	return nil
}

func (*mockSession) SessionID() string {
	return "test-session"
}

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
			name:           "anonymous session rejects caller with token",
			allowAnonymous: true,
			boundTokenHash: "",
			caller:         &auth.Identity{Subject: "user", Token: "token"},
			wantErr:        sessiontypes.ErrUnauthorizedCaller, // Prevent session upgrade attack
		},
		{
			name:           "bound session with nil caller",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token", testSecret, testTokenSalt),
			caller:         nil,
			wantErr:        sessiontypes.ErrNilCaller,
		},
		{
			name:           "bound session with matching token",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token", testSecret, testTokenSalt),
			caller:         &auth.Identity{Subject: "user", Token: "correct-token"},
			wantErr:        nil, // Should succeed
		},
		{
			name:           "bound session with wrong token",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token", testSecret, testTokenSalt),
			caller:         &auth.Identity{Subject: "user", Token: "wrong-token"},
			wantErr:        sessiontypes.ErrUnauthorizedCaller,
		},
		{
			name:           "bound session with empty token in identity",
			allowAnonymous: false,
			boundTokenHash: HashToken("correct-token", testSecret, testTokenSalt),
			caller:         &auth.Identity{Subject: "user", Token: ""},
			wantErr:        sessiontypes.ErrUnauthorizedCaller,
		},
		{
			name:           "anonymous session accepts caller with empty token",
			allowAnonymous: true,
			boundTokenHash: "",
			caller:         &auth.Identity{Subject: "user", Token: ""},
			wantErr:        nil, // Empty token is equivalent to no token
		},
		{
			name:           "misconfigured bound session with empty hash rejects empty token",
			allowAnonymous: false,
			boundTokenHash: "", // Misconfiguration: bound but no hash
			caller:         &auth.Identity{Subject: "user", Token: ""},
			wantErr:        sessiontypes.ErrSessionOwnerUnknown, // Fail closed
		},
		{
			name:           "misconfigured bound session with empty hash rejects nil caller",
			allowAnonymous: false,
			boundTokenHash: "", // Misconfiguration: bound but no hash
			caller:         nil,
			wantErr:        sessiontypes.ErrNilCaller, // Nil check happens first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a base session
			baseSession := newMockSession("test-session")

			// Wrap with decorator that has the test configuration
			decorator := &HijackPreventionDecorator{
				session:        baseSession,
				allowAnonymous: tt.allowAnonymous,
				boundTokenHash: tt.boundTokenHash,
				tokenSalt:      testTokenSalt,
				hmacSecret:     testSecret,
			}

			// Test validateCaller directly on the decorator
			err := decorator.validateCaller(tt.caller)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestConcurrentValidation tests that validateCaller is safe for concurrent use.
func TestConcurrentValidation(t *testing.T) {
	t.Parallel()

	baseSession := newMockSession("test-session")

	decorator := &HijackPreventionDecorator{
		session:        baseSession,
		allowAnonymous: false,
		boundTokenHash: HashToken("test-token", testSecret, testTokenSalt),
		tokenSalt:      testTokenSalt,
		hmacSecret:     testSecret,
	}

	// Run validation concurrently from multiple goroutines
	// Collect errors in channel to avoid race conditions with testify assertions
	const numGoroutines = 10
	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			caller := &auth.Identity{Subject: "user", Token: "test-token"}
			err := decorator.validateCaller(caller)
			errChan <- err
		}()
	}

	// Wait for all goroutines and assert in main goroutine (thread-safe)
	for i := 0; i < numGoroutines; i++ {
		err := <-errChan
		assert.NoError(t, err, "concurrent validation should succeed")
	}
}

// TestPreventSessionHijacking_BasicFunctionality tests the main entry point.
func TestPreventSessionHijacking_BasicFunctionality(t *testing.T) {
	t.Parallel()

	t.Run("authenticated session", func(t *testing.T) {
		t.Parallel()

		baseSession := newMockSession("test-session")
		identity := &auth.Identity{Subject: "user", Token: "test-token"}

		decorated, err := PreventSessionHijacking(baseSession, testSecret, identity, false)
		require.NoError(t, err)
		require.NotNil(t, decorated)

		// Verify metadata was set (no cast needed - returns concrete type)
		metadata := decorated.GetMetadata()
		assert.NotEmpty(t, metadata[MetadataKeyTokenHash])
		assert.NotEmpty(t, metadata[MetadataKeyTokenSalt])
	})

	t.Run("anonymous session", func(t *testing.T) {
		t.Parallel()

		baseSession := newMockSession("test-session")

		decorated, err := PreventSessionHijacking(baseSession, testSecret, nil, true)
		require.NoError(t, err)
		require.NotNil(t, decorated)

		// Verify metadata was set (empty for anonymous, no cast needed)
		metadata := decorated.GetMetadata()
		assert.Empty(t, metadata[MetadataKeyTokenHash])
		assert.Empty(t, metadata[MetadataKeyTokenSalt])
	})
}
