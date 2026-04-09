// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

func TestRestoreHijackPrevention_NilSession(t *testing.T) {
	t.Parallel()

	restored, err := RestoreHijackPrevention(nil, "somehash", hex.EncodeToString(testTokenSalt), testSecret)
	require.Error(t, err)
	assert.Nil(t, restored)
}

func TestRestoreHijackPrevention_MissingSalt(t *testing.T) {
	t.Parallel()

	// Non-empty tokenHash with empty tokenSaltHex is malformed state.
	base := newMockSession("sess")
	restored, err := RestoreHijackPrevention(base, "nonemptyhash", "", testSecret)
	require.Error(t, err)
	assert.Nil(t, restored)
}

func TestRestoreHijackPrevention_InvalidSaltHex(t *testing.T) {
	t.Parallel()

	base := newMockSession("sess")
	restored, err := RestoreHijackPrevention(base, "nonemptyhash", "gg", testSecret)
	require.Error(t, err)
	assert.Nil(t, restored)
}

func TestRestoreHijackPrevention_AnonymousSession(t *testing.T) {
	t.Parallel()

	base := newMockSession("sess")
	// tokenHash="" and tokenSaltHex="" → anonymous.
	restored, err := RestoreHijackPrevention(base, "", "", testSecret)
	require.NoError(t, err)
	require.NotNil(t, restored)

	ctx := context.Background()

	// Nil caller is accepted.
	_, err = restored.CallTool(ctx, nil, "tool", nil, nil)
	require.NoError(t, err)

	// Caller presenting a token is rejected (session upgrade attack prevention).
	caller := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "u"}, Token: "t"}
	_, err = restored.CallTool(ctx, caller, "tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
}

func TestRestoreHijackPrevention_AuthenticatedRoundTrip(t *testing.T) {
	t.Parallel()

	// --- "Pod A": create a session, persist hash+salt from metadata. ---
	base := newMockSession("sess")
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: "bearer-token"}

	created, err := PreventSessionHijacking(base, testSecret, identity)
	require.NoError(t, err)

	meta := created.GetMetadata()
	persistedHash := meta[metadataKeyTokenHash]
	persistedSalt := meta[metadataKeyTokenSalt]
	require.NotEmpty(t, persistedHash, "tokenHash must be persisted")
	require.NotEmpty(t, persistedSalt, "tokenSalt must be persisted")

	// --- "Pod B": restore decorator from persisted values. ---
	base2 := newMockSession("sess")
	restored, err := RestoreHijackPrevention(base2, persistedHash, persistedSalt, testSecret)
	require.NoError(t, err)
	require.NotNil(t, restored)

	ctx := context.Background()

	// Original token is accepted.
	_, err = restored.CallTool(ctx, identity, "tool", nil, nil)
	require.NoError(t, err)

	// A different token is rejected.
	other := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: "wrong-token"}
	_, err = restored.CallTool(ctx, other, "tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)

	// Nil caller is rejected for a bound session.
	_, err = restored.CallTool(ctx, nil, "tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrNilCaller)
}

func TestRestoreHijackPrevention_CrossReplicaSecretMismatch(t *testing.T) {
	t.Parallel()

	// Pod A creates with secretA.
	secretA := []byte("secret-A")
	secretB := []byte("secret-B")

	base := newMockSession("sess")
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: "token"}

	created, err := PreventSessionHijacking(base, secretA, identity)
	require.NoError(t, err)

	meta := created.GetMetadata()
	persistedHash := meta[metadataKeyTokenHash]
	persistedSalt := meta[metadataKeyTokenSalt]

	// Pod B restores with a different secretB — token validation must fail.
	base2 := newMockSession("sess")
	restored, err := RestoreHijackPrevention(base2, persistedHash, persistedSalt, secretB)
	require.NoError(t, err) // Construction succeeds; mismatch only shows at validation time.

	ctx := context.Background()
	_, err = restored.CallTool(ctx, identity, "tool", nil, nil)
	require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller,
		"cross-replica secret mismatch must reject the original token")
}
