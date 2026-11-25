// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// mockSession is a minimal implementation of MultiSession for testing.
// It embeds the interface so only the methods exercised by tests need to be defined.
type mockSession struct {
	sessiontypes.MultiSession // satisfies the rest of the interface
	metadata                  map[string]string
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
	return &vmcp.ToolCallResult{}, nil
}

func (*mockSession) ReadResource(_ context.Context, _ *auth.Identity, _ string) (*vmcp.ResourceReadResult, error) {
	return &vmcp.ResourceReadResult{}, nil
}

func (*mockSession) GetPrompt(_ context.Context, _ *auth.Identity, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return &vmcp.PromptGetResult{}, nil
}

func (*mockSession) Close() error { return nil }

// newDecoratedSession creates a mockSession wrapped with BindSession using the given identity.
func newDecoratedSession(t *testing.T, identity *auth.Identity) sessiontypes.MultiSession {
	t.Helper()
	base := newMockSession("test-session")
	decorated, err := BindSession(base, identity)
	require.NoError(t, err)
	require.NotNil(t, decorated)
	return decorated
}

// authedIdentity builds an authenticated identity with the given issuer and subject.
// token is used as the raw bearer token (may be any non-empty string).
func authedIdentity(token, iss, sub string) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Claims: map[string]any{
				"iss": iss,
				"sub": sub,
			},
		},
		Token: token,
	}
}

// identityWithClaims builds an *auth.Identity whose Claims map is set verbatim
// from claims. Used in tests that need malformed claim values (missing keys,
// non-string values, NUL bytes) that authedIdentity cannot express.
func identityWithClaims(token string, claims map[string]any) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Claims: claims},
		Token:         token,
	}
}

// TestBindSession_AcceptsRefreshedTokenWithSameIdentity is the regression test
// for issue #5306. A caller presenting a new bearer token (refreshed access
// token) but the same (iss, sub) identity must be accepted because validation
// is now identity-based, not token-hash-based.
func TestBindSession_AcceptsRefreshedTokenWithSameIdentity(t *testing.T) {
	t.Parallel()

	const iss = "https://idp.example"
	const sub = "user-42"

	// Session created with AT1.
	creator := authedIdentity("AT1", iss, sub)
	decorated := newDecoratedSession(t, creator)

	// Subsequent call arrives with AT2 (refreshed token) but same (iss, sub).
	refreshed := authedIdentity("AT2", iss, sub)
	_, err := decorated.CallTool(context.Background(), refreshed, "tool", nil, nil)
	require.NoError(t, err, "refreshed token with same identity must be accepted")
}

func TestBindSession_RejectsInvalidIdentityAtCreation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *auth.Identity
	}{
		{name: "missing_sub_claim", identity: identityWithClaims("tok", map[string]any{"iss": "https://idp.example"})},
		{name: "missing_iss_claim", identity: identityWithClaims("tok", map[string]any{"sub": "alice"})},
		{name: "both_claims_empty", identity: identityWithClaims("tok", map[string]any{})},
		{name: "non_string_iss", identity: identityWithClaims("tok", map[string]any{"iss": 42, "sub": "alice"})},
		{name: "non_string_sub", identity: identityWithClaims("tok", map[string]any{"iss": "https://idp.example", "sub": true})},
		{name: "nul_byte_in_iss", identity: identityWithClaims("tok", map[string]any{"iss": "bad\x00iss", "sub": "alice"})},
		{name: "nul_byte_in_sub", identity: identityWithClaims("tok", map[string]any{"iss": "https://idp.example", "sub": "bad\x00sub"})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := &metadataObservingSession{mockSession: newMockSession("test-session")}
			decorated, err := BindSession(base, tt.identity)

			require.Error(t, err)
			assert.Nil(t, decorated)
			assert.False(t, base.setMetadataCalled, "SetMetadata must not be called if binding extraction fails")
		})
	}
}

func TestBindSession_RejectsMismatchedCaller(t *testing.T) {
	t.Parallel()

	const boundIss = "https://idp.example"
	const boundSub = "alice"

	tests := []struct {
		name   string
		caller *auth.Identity
	}{
		{name: "different_sub", caller: authedIdentity("tok2", boundIss, "bob")},
		{name: "different_iss", caller: authedIdentity("tok2", "https://idp-b.example", boundSub)},
		{name: "missing_iss_claim", caller: identityWithClaims("tok2", map[string]any{"sub": boundSub})},
		{name: "missing_sub_claim", caller: identityWithClaims("tok2", map[string]any{"iss": boundIss})},
		{name: "both_claims_empty", caller: identityWithClaims("tok2", map[string]any{})},
		{name: "non_string_iss_claim", caller: identityWithClaims("tok2", map[string]any{"iss": []string{boundIss}, "sub": boundSub})},
		{name: "non_string_sub_claim", caller: identityWithClaims("tok2", map[string]any{"iss": boundIss, "sub": 12345})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decorated := newDecoratedSession(t, authedIdentity("tok", boundIss, boundSub))
			_, err := decorated.CallTool(context.Background(), tt.caller, "tool", nil, nil)
			require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
		})
	}
}

func TestBindSession_AnonymousSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		assertFn func(t *testing.T, decorated sessiontypes.MultiSession)
	}{
		{
			name: "nil_identity_stores_sentinel",
			assertFn: func(t *testing.T, decorated sessiontypes.MultiSession) {
				t.Helper()
				meta := decorated.GetMetadata()
				assert.Equal(t, binding.UnauthenticatedSentinel, meta[sessiontypes.MetadataKeyIdentityBinding],
					"anonymous session must store UnauthenticatedSentinel in metadata")
			},
		},
		{
			name: "rejects_caller_presenting_token",
			assertFn: func(t *testing.T, decorated sessiontypes.MultiSession) {
				t.Helper()
				caller := &auth.Identity{Token: "some-token"}
				_, err := decorated.CallTool(context.Background(), caller, "tool", nil, nil)
				require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
			},
		},
		{
			name: "accepts_nil_caller",
			assertFn: func(t *testing.T, decorated sessiontypes.MultiSession) {
				t.Helper()
				_, err := decorated.CallTool(context.Background(), nil, "tool", nil, nil)
				require.NoError(t, err)
			},
		},
		{
			name: "accepts_caller_with_empty_token",
			assertFn: func(t *testing.T, decorated sessiontypes.MultiSession) {
				t.Helper()
				caller := &auth.Identity{Token: ""}
				_, err := decorated.CallTool(context.Background(), caller, "tool", nil, nil)
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := newMockSession("test-session")
			decorated, err := BindSession(base, nil)
			require.NoError(t, err)
			require.NotNil(t, decorated)
			tt.assertFn(t, decorated)
		})
	}
}

func TestBindSession_NilSession(t *testing.T) {
	t.Parallel()

	identity := authedIdentity("tok", "https://idp.example", "alice")
	decorated, err := BindSession(nil, identity)
	require.Error(t, err)
	assert.Nil(t, decorated)
}

func TestBindSession_BoundRejectsNilCaller(t *testing.T) {
	t.Parallel()

	decorated := newDecoratedSession(t, authedIdentity("tok", "https://idp.example", "alice"))

	_, err := decorated.CallTool(context.Background(), nil, "tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrNilCaller)
}

// TestBindSession_ConcurrentRefreshRace verifies that two goroutines calling
// CallTool concurrently with different bearer tokens but the same (iss, sub)
// both succeed. This tests the core fix for issue #5306.
func TestBindSession_ConcurrentRefreshRace(t *testing.T) {
	t.Parallel()

	const iss = "https://idp.example"
	const sub = "alice"

	decorated := newDecoratedSession(t, authedIdentity("AT1", iss, sub))

	const goroutines = 20

	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			// Each goroutine uses a distinct token string but the same identity.
			caller := authedIdentity("refreshed-token-"+string(rune('A'+i%26)), iss, sub)
			_, errs[i] = decorated.CallTool(context.Background(), caller, "tool", nil, nil)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent CallTool goroutines")
	}

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d must succeed with same identity and different token", i)
	}
}

// metadataObservingSession wraps mockSession and records whether SetMetadata
// was ever called. Used in tests that assert SetMetadata is NOT called before
// a binding is validated.
type metadataObservingSession struct {
	*mockSession
	setMetadataCalled bool
}

func (m *metadataObservingSession) SetMetadata(key, value string) {
	m.setMetadataCalled = true
	m.mockSession.SetMetadata(key, value)
}
