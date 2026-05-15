// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authmocks "github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// okTransport is a minimal RoundTripper that records the received request and
// returns a 200 OK with an empty body.
type okTransport struct {
	received *http.Request
}

func (t *okTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.received = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(nil),
	}, nil
}

// newTestRequest creates a GET request to a fixed URL using the provided context.
func newTestRequest(ctx context.Context, t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://backend.example.com/mcp", nil)
	require.NoError(t, err)
	return req
}

// ---------------------------------------------------------------------------
// httpRoundTripperFunc
// ---------------------------------------------------------------------------

func TestHTTPRoundTripperFunc_DelegatesToWrappedFunction(t *testing.T) {
	t.Parallel()

	called := false
	wantResp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(nil)}

	rt := httpRoundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return wantResp, nil
	})

	req := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(req)

	require.NoError(t, err)
	assert.True(t, called, "wrapped function was not called")
	assert.Same(t, wantResp, resp)
}

func TestHTTPRoundTripperFunc_PropagatesError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("transport error")
	rt := httpRoundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, wantErr
	})

	req := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(req)

	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, resp)
}

// ---------------------------------------------------------------------------
// authRoundTripper
// ---------------------------------------------------------------------------

func TestAuthRoundTripper_SuccessfulAuth_ForwardsRequestToBase(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockStrat := authmocks.NewMockStrategy(ctrl)

	authConfig := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}
	target := &vmcp.BackendTarget{WorkloadID: "backend-a"}

	base := &okTransport{}
	rt := &authRoundTripper{
		base:         base,
		authStrategy: mockStrat,
		authConfig:   authConfig,
		target:       target,
	}

	req := newTestRequest(context.Background(), t)
	mockStrat.EXPECT().Authenticate(gomock.Any(), gomock.Any(), authConfig).Return(nil)

	resp, err := rt.RoundTrip(req)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// The request forwarded to base must be a clone, not the original.
	require.NotNil(t, base.received)
	assert.NotSame(t, req, base.received, "base received the original request, expected a clone")
}

func TestAuthRoundTripper_AuthFailure_ReturnsErrorAndSkipsBase(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockStrat := authmocks.NewMockStrategy(ctrl)

	authConfig := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}
	target := &vmcp.BackendTarget{WorkloadID: "backend-b"}

	baseCalled := false
	base := httpRoundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		baseCalled = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(nil)}, nil
	})

	authErr := errors.New("token expired")
	mockStrat.EXPECT().Authenticate(gomock.Any(), gomock.Any(), authConfig).Return(authErr)

	rt := &authRoundTripper{
		base:         base,
		authStrategy: mockStrat,
		authConfig:   authConfig,
		target:       target,
	}

	req := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(req)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.False(t, baseCalled, "base transport should not be called when auth fails")

	// Error must mention the backend ID so operators can identify the failure.
	assert.ErrorContains(t, err, "backend-b")
	assert.ErrorContains(t, err, "token expired")
}

func TestAuthRoundTripper_AuthStrategyReceivesClonedRequest(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockStrat := authmocks.NewMockStrategy(ctrl)

	target := &vmcp.BackendTarget{WorkloadID: "backend-c"}
	authConfig := &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}

	var strategyReq *http.Request
	mockStrat.EXPECT().
		Authenticate(gomock.Any(), gomock.Any(), authConfig).
		DoAndReturn(func(_ context.Context, req *http.Request, _ *authtypes.BackendAuthStrategy) error {
			strategyReq = req
			return nil
		})

	base := &okTransport{}
	rt := &authRoundTripper{
		base:         base,
		authStrategy: mockStrat,
		authConfig:   authConfig,
		target:       target,
	}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	// Strategy must receive the cloned request, not the original.
	require.NotNil(t, strategyReq)
	assert.NotSame(t, orig, strategyReq, "strategy received the original request, expected a clone")
}

// ---------------------------------------------------------------------------
// identityRoundTripper
// ---------------------------------------------------------------------------
//
// See identityPropagatingRoundTripper in pkg/vmcp/client/client.go for the
// canonical description of the #5323 fallback-only identity invariant. The
// session-backed twin in mcp_session.go omits health-check propagation
// because health probes do not flow through this connector; if that ever
// changes, mirror the health-check test set from client_test.go here.
//
// Tests below are grouped to reflect this hierarchy:
//   1. Per-request identity (normal path)            — *_PerRequestIdentity_*
//   2. Fallback identity (no-identity-on-context)    — *_FallbackIdentity_*

// --- Per-request identity (normal path) -----------------------------------

// TestIdentityRoundTripper_PerRequestIdentity_FlowsThroughUnchanged verifies
// the normal per-request flow: identity is on req.Context() (placed by
// TokenValidator.Middleware), no fallback is configured, and the transport
// leaves the identity untouched so the freshly refreshed upstream tokens
// reach the downstream auth strategies. This is the path taken on every
// authenticated tool call.
func TestIdentityRoundTripper_PerRequestIdentity_FlowsThroughUnchanged(t *testing.T) {
	t.Parallel()

	base := &okTransport{}
	rt := &identityRoundTripper{base: base, fallbackIdentity: nil}

	fresh := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "fresh-user"},
		UpstreamTokens: map[string]string{"provider": "fresh-token"},
	}
	ctx := auth.WithIdentity(context.Background(), fresh)
	orig := newTestRequest(ctx, t)

	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	require.NotNil(t, base.received)
	got, ok := auth.IdentityFromContext(base.received.Context())
	require.True(t, ok)
	assert.Equal(t, "fresh-user", got.Subject)
	assert.Equal(t, "fresh-token", got.UpstreamTokens["provider"])
}

// TestIdentityRoundTripper_PerRequestIdentity_NotOverriddenByFallback is the
// regression test for issue #5323: a fresh identity already on req.Context()
// (placed there by TokenValidator.Middleware on every incoming request) must
// survive the transport untouched, even when a stale fallback identity is
// captured. Overriding it would silently re-inject stale upstream tokens on
// every backend call, forcing daily re-auth.
func TestIdentityRoundTripper_PerRequestIdentity_NotOverriddenByFallback(t *testing.T) {
	t.Parallel()

	stale := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "stale-user"},
		UpstreamTokens: map[string]string{"provider": "stale-token"},
	}
	base := &okTransport{}
	rt := &identityRoundTripper{base: base, fallbackIdentity: stale}

	fresh := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "fresh-user"},
		UpstreamTokens: map[string]string{"provider": "fresh-token"},
	}
	ctx := auth.WithIdentity(context.Background(), fresh)
	orig := newTestRequest(ctx, t)

	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	require.NotNil(t, base.received)
	got, ok := auth.IdentityFromContext(base.received.Context())
	require.True(t, ok, "identity from req.Context() must be present downstream")
	assert.Equal(t, "fresh-user", got.Subject,
		"identity on req.Context() must not be overridden by the captured fallback (#5323)")
	assert.Equal(t, "fresh-token", got.UpstreamTokens["provider"],
		"fresh upstream tokens must reach auth strategies unchanged (#5323)")
}

// --- Fallback identity (teardown / no-identity-on-context path) ------------

// TestIdentityRoundTripper_FallbackIdentity_InjectedWhenContextLacksIdentity
// verifies that the captured fallback identity is injected when req.Context()
// carries no identity (e.g. mcp-go's Close() DELETE built from context.Background()).
func TestIdentityRoundTripper_FallbackIdentity_InjectedWhenContextLacksIdentity(t *testing.T) {
	t.Parallel()

	fallback := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-42"}}
	base := &okTransport{}
	rt := &identityRoundTripper{base: base, fallbackIdentity: fallback}

	orig := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(orig)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Downstream request must carry the fallback identity in its context.
	require.NotNil(t, base.received)
	got, ok := auth.IdentityFromContext(base.received.Context())
	require.True(t, ok, "fallback identity not found in downstream request context")
	assert.Equal(t, "user-42", got.Subject)

	// Original request context must be unmodified.
	_, origOk := auth.IdentityFromContext(orig.Context())
	assert.False(t, origOk, "original request context was mutated")
}

func TestIdentityRoundTripper_FallbackIdentity_NilFallback_ContextUnchanged(t *testing.T) {
	t.Parallel()

	base := &okTransport{}
	rt := &identityRoundTripper{base: base, fallbackIdentity: nil}

	orig := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(orig)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// No identity should be present in the downstream context.
	require.NotNil(t, base.received)
	_, ok := auth.IdentityFromContext(base.received.Context())
	assert.False(t, ok, "identity unexpectedly found in context when no fallback and no req-ctx identity")
}

func TestIdentityRoundTripper_FallbackIdentity_InjectionClonesRequest(t *testing.T) {
	t.Parallel()

	fallback := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-99"}}
	base := &okTransport{}
	rt := &identityRoundTripper{base: base, fallbackIdentity: fallback}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	// When the fallback is injected, the request must be cloned so the original
	// request's context remains untouched.
	require.NotNil(t, base.received)
	assert.NotSame(t, orig, base.received, "fallback injection should clone the request")
}

// ---------------------------------------------------------------------------
// claimInjectionRoundTripper
// ---------------------------------------------------------------------------

func TestClaimInjectionRoundTripper_AllFields_InjectsHeaders(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "108352771234567890",
		Email:   "user@example.com",
		Name:    "Test User",
	}}
	base := &okTransport{}
	rt := &claimInjectionRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(orig)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.NotNil(t, base.received)
	assert.Equal(t, "108352771234567890", base.received.Header.Get("X-User-Sub"))
	assert.Equal(t, "user@example.com", base.received.Header.Get("X-User-Email"))
	assert.Equal(t, "Test User", base.received.Header.Get("X-User-Name"))
}

func TestClaimInjectionRoundTripper_EmptyEmail_DoesNotInjectEmailHeader(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "sub-only",
		// Email and Name intentionally omitted.
	}}
	base := &okTransport{}
	rt := &claimInjectionRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	require.NotNil(t, base.received)
	assert.Equal(t, "sub-only", base.received.Header.Get("X-User-Sub"), "X-User-Sub must be set")
	assert.Empty(t, base.received.Header.Get("X-User-Email"), "X-User-Email must not be set when empty")
	assert.Empty(t, base.received.Header.Get("X-User-Name"), "X-User-Name must not be set when empty")
}

func TestClaimInjectionRoundTripper_EmptySubject_DoesNotInjectSubHeader(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		// Subject intentionally omitted.
		Email: "user@example.com",
	}}
	base := &okTransport{}
	rt := &claimInjectionRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	require.NotNil(t, base.received)
	assert.Empty(t, base.received.Header.Get("X-User-Sub"), "X-User-Sub must not be set when subject is empty")
	assert.Equal(t, "user@example.com", base.received.Header.Get("X-User-Email"))
}

func TestClaimInjectionRoundTripper_ClonesRequest_OriginalUnmodified(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "clone-test",
		Email:   "clone@example.com",
	}}
	base := &okTransport{}
	rt := &claimInjectionRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	// The forwarded request must be a distinct clone, not the original.
	require.NotNil(t, base.received)
	assert.NotSame(t, orig, base.received, "claimInjectionRoundTripper must clone the request")

	// The original request must not be mutated.
	assert.Empty(t, orig.Header.Get("X-User-Sub"), "original request header must not be mutated")
}
