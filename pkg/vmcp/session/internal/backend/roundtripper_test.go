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

func TestIdentityRoundTripper_WithIdentity_PropagatesIdentityInContext(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{Subject: "user-42"}
	base := &okTransport{}
	rt := &identityRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(orig)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Downstream request must carry the identity in its context.
	require.NotNil(t, base.received)
	got, ok := auth.IdentityFromContext(base.received.Context())
	require.True(t, ok, "identity not found in downstream request context")
	assert.Equal(t, "user-42", got.Subject)

	// Original request context must be unmodified.
	_, origOk := auth.IdentityFromContext(orig.Context())
	assert.False(t, origOk, "original request context was mutated")
}

func TestIdentityRoundTripper_NilIdentity_ContextUnchanged(t *testing.T) {
	t.Parallel()

	base := &okTransport{}
	rt := &identityRoundTripper{base: base, identity: nil}

	orig := newTestRequest(context.Background(), t)
	resp, err := rt.RoundTrip(orig)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// No identity should be present in the downstream context.
	require.NotNil(t, base.received)
	_, ok := auth.IdentityFromContext(base.received.Context())
	assert.False(t, ok, "identity unexpectedly found in context when nil identity was configured")
}

func TestIdentityRoundTripper_WithIdentity_ClonesRequest(t *testing.T) {
	t.Parallel()

	identity := &auth.Identity{Subject: "user-99"}
	base := &okTransport{}
	rt := &identityRoundTripper{base: base, identity: identity}

	orig := newTestRequest(context.Background(), t)
	_, err := rt.RoundTrip(orig)
	require.NoError(t, err)

	// A non-nil identity must cause the request to be cloned.
	require.NotNil(t, base.received)
	assert.NotSame(t, orig, base.received, "non-nil identity should clone the request")
}
