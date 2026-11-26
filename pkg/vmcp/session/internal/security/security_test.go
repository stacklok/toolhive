// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/security"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
	"github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

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
			identity: &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: "token"},
			want:     false,
		},
		{
			name:     "non-nil identity with empty token",
			identity: &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user"}, Token: ""},
			want:     true, // Empty token is treated as anonymous
		},
		{
			name:     "non-nil identity with empty subject",
			identity: &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: ""}, Token: "token"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sessiontypes.ShouldAllowAnonymous(tt.identity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveBinding(t *testing.T) {
	t.Parallel()

	for _, tc := range bindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := security.ResolveBinding(tc.identity)
			assert.Equal(t, tc.want, got)
			if tc.want == "" {
				assert.ErrorIs(t, err, sessionbinding.ErrInvalidBinding)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBindSession(t *testing.T) {
	t.Parallel()

	t.Run("writes delegated binding", func(t *testing.T) {
		t.Parallel()
		base := mocks.NewMockMultiSession(gomock.NewController(t))
		base.EXPECT().SetMetadata(sessiontypes.MetadataKeyIdentityBinding, delegatedActorBinding)
		decorated, err := security.BindSession(base, delegatedActorBinding)
		require.NoError(t, err)
		assert.NotNil(t, decorated)
	})
	invalid := []struct {
		name      string
		bindingID string
	}{
		{"empty", ""},
		{"no NUL separator", "no-nul"},
		{"incomplete delegated", "iss\x00sub\x00delegated\x00client\x00act-iss"},
	}
	for _, tc := range invalid {
		t.Run("invalid binding fails without writing metadata: "+tc.name, func(t *testing.T) {
			t.Parallel()
			base := mocks.NewMockMultiSession(gomock.NewController(t))
			decorated, err := security.BindSession(base, tc.bindingID)
			require.ErrorIs(t, err, sessionbinding.ErrInvalidBinding)
			assert.Nil(t, decorated)
		})
	}
}

type bindingCase struct {
	name     string
	identity *auth.Identity
	want     string
}

func bindingCases() []bindingCase {
	return []bindingCase{
		{"nil identity", nil, sessionbinding.UnauthenticatedSentinel},
		{"empty non-delegated identity", &auth.Identity{}, sessionbinding.UnauthenticatedSentinel},
		{"without actor", delegatedCaller("client", nil), delegatedBinding},
		{"with actor", delegatedCaller("client", testActor()), delegatedActorBinding},
		{"missing client", delegatedCaller("", nil), ""},
		{"tokenless unknown route", &auth.Identity{AdmissionRoute: "custom", PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{}}}, ""},
	}
}
