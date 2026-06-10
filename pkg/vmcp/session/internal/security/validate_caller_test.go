// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// boundIdentity builds an authenticated identity carrying the given (iss, sub) claims.
func boundIdentity(token, iss, sub string) *auth.Identity {
	return &auth.Identity{
		Token: token,
		PrincipalInfo: auth.PrincipalInfo{
			Claims: map[string]any{"iss": iss, "sub": sub},
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

	const iss, sub = "https://idp.example", "user-42"
	bound, err := binding.Format(iss, sub)
	require.NoError(t, err)

	tests := []struct {
		name          string
		storedBinding string
		caller        *auth.Identity
		wantErr       error
	}{
		{"authenticated match (refreshed token, same identity)", bound, boundIdentity("AT2", iss, sub), nil},
		{"authenticated mismatch", bound, boundIdentity("AT", iss, "someone-else"), sessiontypes.ErrUnauthorizedCaller},
		{"authenticated nil caller", bound, nil, sessiontypes.ErrNilCaller},
		{"anonymous nil caller", binding.UnauthenticatedSentinel, nil, nil},
		{"anonymous caller without token", binding.UnauthenticatedSentinel, &auth.Identity{}, nil},
		{"anonymous upgrade attack", binding.UnauthenticatedSentinel, &auth.Identity{Token: "tok"}, sessiontypes.ErrUnauthorizedCaller},
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
