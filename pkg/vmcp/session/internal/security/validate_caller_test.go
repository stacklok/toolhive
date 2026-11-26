// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// testIssuer is the fixed OIDC issuer used to build test identities/bindings.
const testIssuer = "https://idp.example"

// boundIdentity builds an authenticated identity carrying the testIssuer/sub claims.
func boundIdentity(token, sub string) *auth.Identity {
	return &auth.Identity{
		Token: token,
		PrincipalInfo: auth.PrincipalInfo{
			Claims: map[string]any{"iss": testIssuer, "sub": sub},
		},
	}
}

// TestValidateCaller covers the exported binding-validation seam used by call paths
// that do not flow through the BindSession decorator (the Serve transport path). It
// verifies that allowAnonymous is derived correctly from the stored binding string:
// the sentinel admits anonymous callers and rejects token-upgrade attacks, while a
// real binding requires a matching (iss, sub) caller.
func TestValidateCaller(t *testing.T) {
	t.Parallel()

	const sub = "user-42"
	bound, err := binding.Format(testIssuer, sub)
	require.NoError(t, err)

	tests := []struct {
		name          string
		storedBinding string
		caller        *auth.Identity
		wantErr       error
	}{
		{"authenticated match (refreshed token, same identity)", bound, boundIdentity("AT2", sub), nil},
		{"authenticated mismatch", bound, boundIdentity("AT", "someone-else"), sessiontypes.ErrUnauthorizedCaller},
		{"authenticated nil caller", bound, nil, sessiontypes.ErrNilCaller},
		{"anonymous nil caller", binding.UnauthenticatedSentinel, nil, nil},
		{"anonymous caller without token", binding.UnauthenticatedSentinel, &auth.Identity{}, nil},
		{"anonymous upgrade attack", binding.UnauthenticatedSentinel, &auth.Identity{Token: "tok"}, sessiontypes.ErrUnauthorizedCaller},
		// Fail-closed on corrupted metadata: a non-sentinel binding that does not parse
		// (empty string, or a value with no NUL separator) must reject, never be treated
		// as anonymous. This is the only binding check on the Serve call path.
		{"empty binding fails closed", "", boundIdentity("AT", sub), sessiontypes.ErrSessionOwnerUnknown},
		{"unparsable binding fails closed", "garbage-no-separator", boundIdentity("AT", sub), sessiontypes.ErrSessionOwnerUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidateCaller(tc.storedBinding, tc.caller)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestValidateCaller_Delegated(t *testing.T) {
	t.Parallel()

	interactive := boundIdentity("token", "user-42")
	interactive.AdmissionRoute = auth.AdmissionRouteInteractive
	refreshed := func() *auth.Identity {
		caller := delegatedCaller("client", nil)
		caller.Token = "renewed-token"
		caller.Claims["jti"] = "renewed-id"
		caller.Claims["exp"] = 2000
		caller.Claims["authorization_details"] = []any{map[string]any{"type": "changed"}}
		caller.DelegationChain = audit.ParseDelegationChain(map[string]any{
			"iss": "actor-issuer", "sub": "actor",
			"act": map[string]any{"iss": "prior-issuer", "sub": "changed-prior-actor"},
		}, auth.DefaultMaxDelegationDepth)
		return caller
	}

	for _, tc := range []struct {
		name, stored string
		caller       *auth.Identity
		wantErr      error
	}{
		{"refreshed with changed nested actor", delegatedActorBinding, refreshed(), nil},
		{"wrong client with actor", delegatedActorBinding, delegatedCaller("other-client", testActor()), sessiontypes.ErrUnauthorizedCaller},
		{"interactive caller on actorless session", delegatedBinding, interactive, sessiontypes.ErrUnauthorizedCaller},
		{"delegated caller on interactive session", standardBinding, delegatedCaller("client", testActor()), sessiontypes.ErrUnauthorizedCaller},
		{"missing client", delegatedBinding, delegatedCaller("", nil), sessiontypes.ErrUnauthorizedCaller},
		{"malformed actor", delegatedActorBinding, delegatedCaller("client", "malformed"), sessiontypes.ErrUnauthorizedCaller},
		{"tokenless delegated caller on anonymous session", sessionbinding.UnauthenticatedSentinel,
			&auth.Identity{AdmissionRoute: auth.AdmissionRouteDelegated}, sessiontypes.ErrUnauthorizedCaller},
		{"tokenless unknown-route caller on anonymous session", sessionbinding.UnauthenticatedSentinel,
			&auth.Identity{AdmissionRoute: "custom"}, sessiontypes.ErrUnauthorizedCaller},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := security.ValidateCaller(tc.stored, tc.caller)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

const (
	standardBinding       = testIssuer + "\x00user-42"
	delegatedBinding      = standardBinding + "\x00delegated\x00client"
	delegatedActorBinding = delegatedBinding + "\x00actor-issuer\x00actor"
)

func delegatedCaller(clientID string, actor any) *auth.Identity {
	caller := boundIdentity("original-token", "user-42")
	caller.AdmissionRoute = auth.AdmissionRouteDelegated
	if clientID != "" {
		caller.Claims["client_id"] = clientID
	}
	if actor != nil {
		caller.Claims["act"] = actor
	}
	return caller
}

func testActor() map[string]any {
	return map[string]any{
		"iss": "actor-issuer", "sub": "actor",
		"act": map[string]any{"iss": "prior-issuer", "sub": "original-prior-actor"},
	}
}
