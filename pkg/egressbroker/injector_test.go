// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/egressbroker"
)

var testIdentity = egressbroker.PodIdentity{
	Issuer:    "https://issuer.example.com",
	Subject:   "user-123",
	SessionID: "session-abc",
	MCPServer: "github-mcp",
}

func mustInjector(t *testing.T, reader upstreamtoken.TokenReader) *egressbroker.CredentialInjector {
	t.Helper()
	policy := mustParse(t, testPolicyYAML)
	inj, err := egressbroker.NewCredentialInjector(testIdentity, policy, reader)
	require.NoError(t, err)
	return inj
}

func TestCredentialInjector(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	githubDest := egressbroker.Destination{Host: "api.github.com", Method: "GET", Path: "/repos/foo"}

	t.Run("happy path injects Authorization header", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, got *storage.ExpectedBinding,
			) (map[string]upstreamtoken.UpstreamCredential, []string, error) {
				// The binding must carry the pod-owner's raw sub and be forced Strict.
				assert.Equal(t, "user-123", got.UserID)
				assert.True(t, got.Strict, "injector must force Strict binding via NewStrictTokenReader")
				return map[string]upstreamtoken.UpstreamCredential{
					"github": {AccessToken: "gho_secret"},
				}, nil, nil
			})

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.True(t, decision.Allow)
		assert.Equal(t, egressbroker.AuthorizationHeader, decision.HeaderName)
		assert.Equal(t, "Bearer gho_secret", decision.HeaderValue)
		assert.Empty(t, decision.Reason)
	})

	t.Run("D5 ordering: credential load never happens on policy deny", func(t *testing.T) {
		t.Parallel()

		denyDests := map[string]struct {
			dest   egressbroker.Destination
			reason egressbroker.DenyReason
		}{
			"non-allowlisted host": {
				egressbroker.Destination{Host: "evil.example.com", Method: "GET", Path: "/"},
				egressbroker.DenyReasonNoPolicy,
			},
			"superdomain of allowed": {
				egressbroker.Destination{Host: "github.com", Method: "GET", Path: "/"},
				egressbroker.DenyReasonNoPolicy,
			},
			"disallowed method": {
				egressbroker.Destination{Host: "api.github.com", Method: "DELETE", Path: "/repos/foo"},
				egressbroker.DenyReasonMethodNotAllowed,
			},
			"disallowed path": {
				egressbroker.Destination{Host: "api.github.com", Method: "GET", Path: "/admin"},
				egressbroker.DenyReasonPathNotAllowed,
			},
			"method default is read-only": {
				egressbroker.Destination{Host: "slack.com", Method: "POST", Path: "/api/chat"},
				egressbroker.DenyReasonMethodNotAllowed,
			},
		}
		for name, tc := range denyDests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ctrl := gomock.NewController(t)
				reader := mocks.NewMockTokenReader(ctrl)
				// No EXPECT: any call to the token reader fails the test.
				// This proves D5 is evaluated before any credential load.
				decision := mustInjector(t, reader).Evaluate(ctx, tc.dest, "req-1")
				assert.False(t, decision.Allow)
				assert.Equal(t, tc.reason, decision.Reason, "the deny reason names the failing policy dimension")
				assert.Empty(t, decision.HeaderName, "deny path must never carry header material")
				assert.Empty(t, decision.HeaderValue, "deny path must never carry header material")
			})
		}
	})

	t.Run("non-allowlisted host gets no credential even with a valid session", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		// The reader would return a valid credential for ANY provider — but the
		// destination binding must deny before it is ever consulted.
		decision := mustInjector(t, reader).Evaluate(ctx,
			egressbroker.Destination{Host: "attacker-controlled.example.com", Method: "GET", Path: "/"}, "req-1")
		assert.False(t, decision.Allow)
		assert.Empty(t, decision.HeaderValue)
	})

	t.Run("credential missing → deny", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{}, nil, nil)

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.False(t, decision.Allow)
		assert.Contains(t, decision.DenyDetail, "re-consent")
		assert.Empty(t, decision.HeaderValue)
	})

	t.Run("credential in failed list → deny", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{}, []string{"github"}, nil)

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.False(t, decision.Allow)
		assert.Empty(t, decision.HeaderValue)
	})

	t.Run("empty access token → deny (no Bearer-with-nothing header)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{
				"github": {AccessToken: ""},
			}, nil, nil)

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.False(t, decision.Allow)
		assert.Empty(t, decision.HeaderValue)
	})

	t.Run("Strict binding error → deny (fail closed)", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(nil, nil, fmt.Errorf("row has no owner: %w", storage.ErrInvalidBinding))

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.False(t, decision.Allow)
		assert.Empty(t, decision.HeaderValue)
		// The deny reason must not leak storage internals.
		assert.NotContains(t, decision.DenyDetail, "row")
	})

	t.Run("store down (Redis error) → deny, never passthrough", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(nil, nil, errors.New("connection refused"))

		decision := mustInjector(t, reader).Evaluate(ctx, githubDest, "req-1")
		assert.False(t, decision.Allow)
		assert.Empty(t, decision.HeaderValue)
	})

	t.Run("wildcard destination carries credential for its own provider only", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reader := mocks.NewMockTokenReader(ctrl)
		reader.EXPECT().
			GetAllUpstreamCredentials(gomock.Any(), "session-abc", gomock.Any()).
			Return(map[string]upstreamtoken.UpstreamCredential{
				"github": {AccessToken: "gho_secret"},
				"slack":  {AccessToken: "xoxb_secret"},
			}, nil, nil)

		// Wildcard github host: github credential, never the slack one.
		decision := mustInjector(t, reader).Evaluate(ctx,
			egressbroker.Destination{Host: "raw.githubusercontent.com", Method: "GET", Path: "/repos/x"}, "req-1")
		assert.True(t, decision.Allow)
		assert.Equal(t, "Bearer gho_secret", decision.HeaderValue)
		assert.NotContains(t, decision.HeaderValue, "xoxb")
	})
}

func TestNewCredentialInjectorValidation(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockTokenReader(ctrl)
	policy := mustParse(t, testPolicyYAML)

	t.Run("nil policy", func(t *testing.T) {
		t.Parallel()
		_, err := egressbroker.NewCredentialInjector(testIdentity, nil, reader)
		require.Error(t, err)
	})
	t.Run("nil token reader", func(t *testing.T) {
		t.Parallel()
		_, err := egressbroker.NewCredentialInjector(testIdentity, policy, nil)
		require.Error(t, err)
	})
	t.Run("incomplete identity", func(t *testing.T) {
		t.Parallel()
		for name, id := range map[string]egressbroker.PodIdentity{
			"empty issuer":    {Issuer: "", Subject: "s", SessionID: "x", MCPServer: "m"},
			"empty subject":   {Issuer: "i", Subject: "", SessionID: "x", MCPServer: "m"},
			"empty session":   {Issuer: "i", Subject: "s", SessionID: "", MCPServer: "m"},
			"empty mcpserver": {Issuer: "i", Subject: "s", SessionID: "x", MCPServer: ""},
		} {
			_, err := egressbroker.NewCredentialInjector(id, policy, reader)
			require.Error(t, err, name)
		}
	})
}
