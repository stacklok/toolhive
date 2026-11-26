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
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
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

// identityWithClaims builds an *auth.Identity with the supplied token and claims,
// leaving the separate Subject field unset.
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
			name:        "delegated_without_actor",
			identity:    delegatedSessionIdentity("client", ""),
			wantBinding: "https://idp.example\x00alice\x00delegated\x00client",
		},
		{
			name:        "delegated_with_actor",
			identity:    delegatedSessionIdentity("client", "actor"),
			wantBinding: "https://idp.example\x00alice\x00delegated\x00client\x00actor-issuer\x00actor",
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
				t.Context(), uuid.New().String(), tt.identity, nil, nil,
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

func TestMakeSession_RejectsUnbindableIdentityBeforeConnecting(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{"token without identifying claims", identityWithClaims("token", nil)},
		{"delegated without client", delegatedSessionIdentity("", "actor")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			connectorCalled := false
			factory := newSessionFactoryWithConnector(func(
				_ context.Context, _ *vmcp.BackendTarget, _ *auth.Identity, _ string, _ internalbk.ListChangedSink,
			) (internalbk.Session, *vmcp.CapabilityList, error) {
				connectorCalled = true
				return nil, nil, nil
			})
			backends := []*vmcp.Backend{{ID: "test-backend", Name: "test-backend"}}
			sess, err := factory.MakeSessionWithID(t.Context(), uuid.NewString(), tc.identity, backends, nil)
			if sess != nil {
				t.Cleanup(func() { assert.NoError(t, sess.Close()) })
			}
			assert.ErrorIs(t, err, sessionbinding.ErrInvalidBinding)
			assert.Nil(t, sess)
			assert.False(t, connectorCalled, "an unbindable identity must not reach the connector")
		})
	}
}

func TestRestoreSession_ErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		storedMetadata  map[string]string
		wantNotFoundErr bool // true → must be ErrSessionNotFound; false → must NOT be
		wantErrContains string
		wantNotContains string
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
		{
			name: "malformed_identity_binding_not_exposed",
			storedMetadata: map[string]string{
				MetadataKeyBackendIDs:      "",
				MetadataKeyIdentityBinding: "private-binding-value-must-not-appear",
			},
			wantErrContains: "stored identity binding is malformed",
			wantNotContains: "private-binding-value-must-not-appear",
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
			if tt.wantNotContains != "" {
				assert.NotContains(t, err.Error(), tt.wantNotContains)
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

func TestRestoreSession_DelegatedBindingRoundTrip(t *testing.T) {
	t.Parallel()

	const clientBinding = "https://idp.example\x00alice\x00delegated\x00client"
	for _, tc := range []struct {
		name, actor, otherActor, binding string
	}{
		{"without actor", "", "actor", clientBinding},
		{"with actor", "actor", "other-actor", clientBinding + "\x00actor-issuer\x00actor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			factory := newSessionFactoryWithConnector(nilBackendConnector())
			id := uuid.NewString()
			owner := delegatedSessionIdentity("client", tc.actor)
			original, err := factory.MakeSessionWithID(ctx, id, owner, nil, nil)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, original.Close()) })

			restored, err := factory.RestoreSession(ctx, id, original.GetMetadata(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, restored.Close()) })
			assert.Equal(t, tc.binding, restored.GetMetadata()[MetadataKeyIdentityBinding])

			interactive := identityWithClaims("interactive-token", map[string]any{"iss": "https://idp.example", "sub": "alice"})
			interactive.AdmissionRoute = auth.AdmissionRouteInteractive
			for _, call := range []struct {
				name    string
				caller  *auth.Identity
				wantErr error
			}{
				{"matching caller reaches routing", owner, ErrToolNotFound},
				{"another client", delegatedSessionIdentity("other-client", tc.actor), sessiontypes.ErrUnauthorizedCaller},
				{"another actor", delegatedSessionIdentity("client", tc.otherActor), sessiontypes.ErrUnauthorizedCaller},
				{"interactive caller", interactive, sessiontypes.ErrUnauthorizedCaller},
			} {
				t.Run(call.name, func(t *testing.T) {
					t.Parallel()
					_, err := restored.CallTool(t.Context(), call.caller, "nonexistent", nil, nil)
					assert.ErrorIs(t, err, call.wantErr)
				})
			}
		})
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
	multiSess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), originalIdentity, nil, nil)
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

func delegatedSessionIdentity(clientID, actor string) *auth.Identity {
	identity := identityWithClaims("delegated-token", map[string]any{"iss": "https://idp.example", "sub": "alice"})
	identity.AdmissionRoute = auth.AdmissionRouteDelegated
	if clientID != "" {
		identity.Claims["client_id"] = clientID
	}
	if actor != "" {
		identity.Claims["act"] = map[string]any{"iss": "actor-issuer", "sub": actor}
	}
	return identity
}
