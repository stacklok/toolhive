// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

func TestRestoreSessionBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		storedBinding string
		wantErr       bool
		// checkFn is called with the restored session when wantErr is false.
		// Leave nil to skip behavioral assertions.
		checkFn func(t *testing.T, restored sessiontypes.MultiSession)
	}{
		{
			name:          "bound_round_trip_accepts_matching_caller",
			storedBinding: "https://idp.example\x00alice",
			checkFn: func(t *testing.T, restored sessiontypes.MultiSession) {
				t.Helper()
				matchingCaller := identityWithClaims("some-token", map[string]any{
					"iss": "https://idp.example",
					"sub": "alice",
				})
				_, err := restored.CallTool(context.Background(), matchingCaller, "tool", nil, nil)
				require.NoError(t, err, "matching (iss, sub) caller must be accepted after restore")

				intruder := identityWithClaims("other-token", map[string]any{
					"iss": "https://idp.example",
					"sub": "bob",
				})
				_, err = restored.CallTool(context.Background(), intruder, "tool", nil, nil)
				require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller)
			},
		},
		{
			name:          "unauthenticated_sentinel_accepts_nil_rejects_token",
			storedBinding: binding.UnauthenticatedSentinel,
			checkFn: func(t *testing.T, restored sessiontypes.MultiSession) {
				t.Helper()
				_, err := restored.CallTool(context.Background(), nil, "tool", nil, nil)
				require.NoError(t, err, "nil caller must be accepted for anonymous sessions")

				caller := &auth.Identity{Token: "some-token"}
				_, err = restored.CallTool(context.Background(), caller, "tool", nil, nil)
				require.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller,
					"caller presenting a token must be rejected (session-upgrade attack prevention)")
			},
		},
		{
			name:          "corrupted_binding_no_nul",
			storedBinding: "no-nul-here",
			wantErr:       true,
		},
		{
			name:          "empty_string_rejected",
			storedBinding: "",
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := newMockSession("sess")
			restored, err := RestoreSessionBinding(base, tt.storedBinding)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, restored)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, restored)
			if tt.checkFn != nil {
				tt.checkFn(t, restored)
			}
		})
	}
}

func TestRestoreSessionBinding_NilSession(t *testing.T) {
	t.Parallel()

	restored, err := RestoreSessionBinding(nil, binding.UnauthenticatedSentinel)
	require.Error(t, err)
	assert.Nil(t, restored)
}
