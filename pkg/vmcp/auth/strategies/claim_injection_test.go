// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

func newReqWithIdentity(t *testing.T, identity *auth.Identity) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	if identity != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), identity))
	}
	return req
}

func strategy(claims ...string) *authtypes.BackendAuthStrategy {
	if len(claims) == 0 {
		return &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeClaimInjection}
	}
	return &authtypes.BackendAuthStrategy{
		Type:           authtypes.StrategyTypeClaimInjection,
		ClaimInjection: &authtypes.ClaimInjectionConfig{Claims: claims},
	}
}

func TestClaimInjectionStrategy_Name(t *testing.T) {
	s := strategies.NewClaimInjectionStrategy()
	assert.Equal(t, "claim_injection", s.Name())
}

func TestClaimInjectionStrategy_DefaultSubOnly(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "108352771234567890",
		Email:   "user@example.com",
		Name:    "Test User",
	}}
	req := newReqWithIdentity(t, identity)

	err := s.Authenticate(req.Context(), req, strategy())

	require.NoError(t, err)
	assert.Equal(t, "108352771234567890", req.Header.Get("X-User-Sub"), "sub injected by default")
	assert.Empty(t, req.Header.Get("X-User-Email"), "email NOT injected by default (opt-in)")
	assert.Empty(t, req.Header.Get("X-User-Name"), "name NOT injected by default (opt-in)")
}

func TestClaimInjectionStrategy_ExplicitClaims(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "108352771234567890",
		Email:   "user@example.com",
		Name:    "Test User",
	}}
	req := newReqWithIdentity(t, identity)

	err := s.Authenticate(req.Context(), req, strategy("sub", "email", "name"))

	require.NoError(t, err)
	assert.Equal(t, "108352771234567890", req.Header.Get("X-User-Sub"))
	assert.Equal(t, "user@example.com", req.Header.Get("X-User-Email"))
	assert.Equal(t, "Test User", req.Header.Get("X-User-Name"))
}

func TestClaimInjectionStrategy_SkipsAnonymous(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	anonymous := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "anonymous",
		Email:   "anonymous@localhost",
	}}
	req := newReqWithIdentity(t, anonymous)

	err := s.Authenticate(req.Context(), req, strategy("sub", "email"))

	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("X-User-Sub"), "anonymous identity must not inject headers")
	assert.Empty(t, req.Header.Get("X-User-Email"))
}

func TestClaimInjectionStrategy_SkipsNoIdentity(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)

	err := s.Authenticate(req.Context(), req, strategy("sub", "email"))

	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("X-User-Sub"))
}

func TestClaimInjectionStrategy_SkipsHealthCheck(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "sub-123"}}
	req := newReqWithIdentity(t, identity)
	req = req.WithContext(healthcontext.WithHealthCheckMarker(req.Context()))

	err := s.Authenticate(req.Context(), req, strategy("sub"))

	require.NoError(t, err)
	assert.Empty(t, req.Header.Get("X-User-Sub"), "health checks must not inject headers")
}

func TestClaimInjectionStrategy_EmptyFieldsNotInjected(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "sub-only"}}
	req := newReqWithIdentity(t, identity)

	err := s.Authenticate(req.Context(), req, strategy("sub", "email", "name"))

	require.NoError(t, err)
	assert.Equal(t, "sub-only", req.Header.Get("X-User-Sub"))
	assert.Empty(t, req.Header.Get("X-User-Email"), "empty email must not produce header")
	assert.Empty(t, req.Header.Get("X-User-Name"), "empty name must not produce header")
}

func TestClaimInjectionStrategy_Validate(t *testing.T) {
	s := strategies.NewClaimInjectionStrategy()
	assert.NoError(t, s.Validate(nil), "nil config is valid (defaults to sub-only)")
	assert.NoError(t, s.Validate(&authtypes.BackendAuthStrategy{}))
}

func TestClaimInjectionStrategy_NilContext(t *testing.T) {
	t.Parallel()
	s := strategies.NewClaimInjectionStrategy()
	req := httptest.NewRequest(http.MethodPost, "/mcp", http.NoBody)
	// No identity in context — should return nil, not panic.
	err := s.Authenticate(context.Background(), req, strategy("sub"))
	assert.NoError(t, err)
}
