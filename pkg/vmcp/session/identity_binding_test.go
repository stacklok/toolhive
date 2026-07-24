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
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// nilBackendConnector is a connector that returns (nil, nil, nil), causing the
// backend to be skipped during init. This lets us exercise session-metadata
// logic without real backend connections.
func nilBackendConnector() backendConnector {
	return func(_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity, _ string, _ internalbk.ListChangedSink) (internalbk.Session, *vmcp.CapabilityList, error) {
		return nil, nil, nil
	}
}

// identityWithClaims builds an *auth.Identity whose Claims map is set verbatim
// from claims. Used in tests that need specific claim values without setting
// the Subject field (binding extraction reads only Claims["iss"] and Claims["sub"]).
func identityWithClaims(token string, claims map[string]any) *auth.Identity {
	return &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Claims: claims},
		Token:         token,
	}
}

func TestMakeSession_StoresIdentityBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		identity    *auth.Identity
		wantBinding string
	}{
		{
			name:        "authenticated_oidc",
			identity:    identityWithClaims("bearer-token", map[string]any{"iss": "https://idp.example", "sub": "user-42"}),
			wantBinding: "https://idp.example\x00user-42",
		},
		{
			name:        "nil_identity_anonymous",
			identity:    nil,
			wantBinding: binding.UnauthenticatedSentinel,
		},
		{
			// LocalUserMiddleware sets Token="" and populates Claims with
			// iss="toolhive-local" and sub=<username>.
			name:        "local_user_shape",
			identity:    identityWithClaims("", map[string]any{"iss": "toolhive-local", "sub": "alice"}),
			wantBinding: "toolhive-local\x00alice",
		},
		{
			// AnonymousMiddleware (dev-only) sets Token="" with iss="toolhive-local"
			// and sub="anonymous". All such sessions share one binding — intentional.
			name:        "anonymous_middleware_shape",
			identity:    identityWithClaims("", map[string]any{"iss": "toolhive-local", "sub": "anonymous"}),
			wantBinding: "toolhive-local\x00anonymous",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := newSessionFactoryWithConnector(nilBackendConnector())
			sess, err := factory.MakeSessionWithID(
				t.Context(), uuid.New().String(), tt.identity, nil,
			)
			require.NoError(t, err)
			require.NotNil(t, sess)
			t.Cleanup(func() { _ = sess.Close() })

			meta := sess.GetMetadata()
			assert.Equal(t, tt.wantBinding, meta[MetadataKeyIdentityBinding],
				"MetadataKeyIdentityBinding must equal expected binding")
		})
	}
}

// TestMakeSession_RejectsBoundSessionWithoutIdentifyingClaims verifies the
// ordering invariant from the factory through to BindSession: creating a session
// with an identity that carries no valid (iss, sub) pair returns an error from
// MakeSessionWithID.
func TestMakeSession_RejectsBoundSessionWithoutIdentifyingClaims(t *testing.T) {
	t.Parallel()

	factory := newSessionFactoryWithConnector(nilBackendConnector())

	// Token is present but Claims are empty, so BindSession's extractBindingID fails.
	identity := identityWithClaims("x", map[string]any{})

	_, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), identity, nil)
	require.Error(t, err, "session creation must fail when bound identity lacks identifying claims")
}

func TestRestoreSession_ErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		storedMetadata  map[string]string
		wantNotFoundErr bool // true → must be ErrSessionNotFound; false → must NOT be
		wantErrContains string
	}{
		{
			// A session carrying the legacy MetadataKeyTokenHash but no
			// MetadataKeyIdentityBinding is invalidated with ErrSessionNotFound
			// so the MCP client can re-initialize.
			name: "legacy_token_hash_only",
			storedMetadata: map[string]string{
				MetadataKeyBackendIDs:             "",
				sessiontypes.MetadataKeyTokenHash: "deadbeefdeadbeef",
			},
			wantNotFoundErr: true,
		},
		{
			// Genuinely corrupted metadata (no binding, no legacy key) must
			// NOT masquerade as a session-not-found; it is a distinct error.
			name: "absent_identity_binding_key",
			storedMetadata: map[string]string{
				MetadataKeyBackendIDs: "",
			},
			wantNotFoundErr: false,
			wantErrContains: "absent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := newSessionFactoryWithConnector(nilBackendConnector())
			_, err := factory.RestoreSession(t.Context(), uuid.New().String(), tt.storedMetadata, nil)
			require.Error(t, err)

			if tt.wantNotFoundErr {
				require.True(t, errors.Is(err, transportsession.ErrSessionNotFound),
					"legacy token-hash-only session must return ErrSessionNotFound")
			} else {
				assert.False(t, errors.Is(err, transportsession.ErrSessionNotFound),
					"corrupted metadata must not return ErrSessionNotFound")
			}
			if tt.wantErrContains != "" {
				assert.Contains(t, err.Error(), tt.wantErrContains)
			}
		})
	}
}

// TestRestoreSession_PreservesIdentityBindingAndAcceptsMatchingCaller verifies
// that after RestoreSession the stored identity binding is preserved in session
// metadata and the security decorator accepts a caller whose identity matches
// the binding.
func TestRestoreSession_PreservesIdentityBindingAndAcceptsMatchingCaller(t *testing.T) {
	t.Parallel()

	factory := newSessionFactoryWithConnector(nilBackendConnector())

	const storedBinding = "https://idp.example\x00alice"
	storedMetadata := map[string]string{
		MetadataKeyBackendIDs:                   "",
		sessiontypes.MetadataKeyIdentityBinding: storedBinding,
	}

	sess, err := factory.RestoreSession(t.Context(), uuid.New().String(), storedMetadata, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() { _ = sess.Close() })

	meta := sess.GetMetadata()
	assert.Equal(t, storedBinding, meta[sessiontypes.MetadataKeyIdentityBinding])

	// Call a tool with the expected identity to verify the decorator accepts it.
	caller := identityWithClaims("any-token", map[string]any{
		"iss": "https://idp.example",
		"sub": "alice",
	})
	_, err = sess.CallTool(t.Context(), caller, "nonexistent", nil, nil)
	// ErrToolNotFound is acceptable — it means auth passed.
	if err != nil {
		assert.ErrorIs(t, err, ErrToolNotFound,
			"restored session must accept matching caller; any error must be ErrToolNotFound (not auth)")
	}
}

// TestRestoreSession_PassesNilIdentityToConnector pins the contract that
// RestoreSession does NOT fabricate a partial *auth.Identity to pass to backend
// connectors. The original bearer token is not persisted; constructing an
// identity with empty Token and UpstreamTokens would violate the field-
// completeness contract on *auth.Identity (see pkg/auth/identity.go) and be a
// landmine for any future consumer that assumes a non-nil identity is fully
// populated. See issue #5336.
//
// Live tool calls already carry a fully-populated identity on req.Context() from
// TokenValidator.Middleware; the connector's nil fallback identity means it never
// injects an incomplete substitute.
func TestRestoreSession_PassesNilIdentityToConnector(t *testing.T) {
	t.Parallel()

	const (
		origIss = "https://idp.example"
		origSub = "carol"
	)

	var (
		connectorCalled  bool
		capturedIdentity *auth.Identity
	)
	capturingConnector := func(
		_ context.Context,
		_ *vmcp.BackendTarget,
		id *auth.Identity,
		_ string,
		_ internalbk.ListChangedSink,
	) (internalbk.Session, *vmcp.CapabilityList, error) {
		connectorCalled = true
		capturedIdentity = id
		return nil, nil, nil
	}

	// Step 1: create the original session with an authenticated identity.
	originalIdentity := identityWithClaims("bearer-AT1", map[string]any{
		"iss": origIss,
		"sub": origSub,
	})

	factory := newSessionFactoryWithConnector(capturingConnector)
	multiSess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), originalIdentity, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = multiSess.Close() })

	// Step 2: capture the persisted metadata (simulates what Redis would hold).
	meta := multiSess.GetMetadata()
	require.NotEmpty(t, meta[sessiontypes.MetadataKeyIdentityBinding],
		"factory must write MetadataKeyIdentityBinding to metadata")

	connectorCalled = false
	capturedIdentity = nil

	// Step 3: restore the session on "Pod B" with a backend present so the
	// connector is actually invoked.
	backend := &vmcp.Backend{
		ID:   "test-backend",
		Name: "test-backend",
	}
	storedMeta := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		storedMeta[k] = v
	}
	storedMeta[MetadataKeyBackendIDs] = backend.ID

	restored, err := factory.RestoreSession(t.Context(), uuid.New().String(), storedMeta, []*vmcp.Backend{backend})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	// Step 4: the connector must be called with nil identity. RestoreSession
	// must not fabricate a partial *auth.Identity when the bearer token is
	// unavailable — passing nil avoids silently supplying empty credentials to
	// outgoing-auth strategies (upstreamInject, tokenExchange, aws_sts) that
	// need UpstreamTokens.
	require.True(t, connectorCalled, "connector must be invoked when a backend is present")
	assert.Nil(t, capturedIdentity,
		"RestoreSession must pass nil identity to the connector — no partial *auth.Identity should be fabricated")
}
