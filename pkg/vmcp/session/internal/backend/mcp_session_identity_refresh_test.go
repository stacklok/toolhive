// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestHTTPSession_PerRequestIdentity_DrivesUpstreamAuthHeader is the
// higher-level regression test for issue #5323. It exercises the full
// transport chain — headerForwardRoundTripper → identityRoundTripper →
// authRoundTripper (UpstreamInjectStrategy) → http.DefaultTransport — and
// asserts that the upstream Authorization header on each tool call reflects
// the identity placed on the per-request context, NOT the identity captured
// at session-init time.
//
// Without the fix, the captured session-init identity would override the
// per-request identity on every outbound tool call, and the upstream would
// see the stale bearer token on the second CallTool. The test would then
// observe the same token on both calls — the symptom users experienced as
// "forced re-auth every ~24h" once the captured access token expired.
//
// Scope: this is the higher-level test called out by the AC5 review feedback.
// It stops short of wiring auth.TokenValidator.Middleware end-to-end (that
// would pull in pkg/auth/upstreamtoken and a JWT signer) and instead injects
// the fresh identity directly onto the per-request context, which is exactly
// what TokenValidator.Middleware does on every authenticated request.
func TestHTTPSession_PerRequestIdentity_DrivesUpstreamAuthHeader(t *testing.T) {
	t.Parallel()

	const (
		providerName = "test-provider"
		// staleToken is captured at session-creation time. If the
		// (pre-fix) bug is reintroduced, the upstream will see this on
		// every tool call.
		staleToken = "stale-access-token"
		// freshToken is placed on the per-request context immediately
		// before each tool call. The post-fix transport must let it
		// reach the upstream unchanged.
		freshToken = "fresh-access-token"
	)

	fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
	url := newFakeBackend(t, fb)

	registry := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy(
		authtypes.StrategyTypeUpstreamInject,
		strategies.NewUpstreamInjectStrategy(),
	))

	target := &vmcp.BackendTarget{
		WorkloadID:    "identity-refresh-backend",
		WorkloadName:  "identity-refresh-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
		AuthConfig: &authtypes.BackendAuthStrategy{
			Type:           authtypes.StrategyTypeUpstreamInject,
			UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: providerName},
		},
	}

	// staleIdentity is captured at session-init time and stored as the
	// fallback. A pre-fix transport would re-inject this on every backend
	// request, regardless of what is on the per-request context.
	staleIdentity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "user-1"},
		UpstreamTokens: map[string]string{providerName: staleToken},
	}

	connector := NewHTTPConnector(registry)
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	sess, _, err := connector(initCtx, target, staleIdentity, "", nil)
	require.NoError(t, err, "connector must initialise the backend successfully")
	t.Cleanup(func() { _ = sess.Close() })

	// CallTool with a fresh identity on the per-request context. This
	// simulates what auth.TokenValidator.Middleware does on every
	// authenticated incoming request: the identity it places on the
	// context carries upstream tokens that were transparently refreshed
	// by upstreamtoken.InProcessService.GetAllUpstreamCredentials.
	freshIdentity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "user-1"},
		UpstreamTokens: map[string]string{providerName: freshToken},
	}
	callCtx, cancelCall := context.WithTimeout(
		auth.WithIdentity(context.Background(), freshIdentity),
		5*time.Second,
	)
	t.Cleanup(cancelCall)

	_, err = sess.CallTool(callCtx, "echo", map[string]any{}, nil)
	require.NoError(t, err, "tools/call with fresh identity must succeed")

	got := fb.headersFor(string(mcp.MethodToolsCall))
	require.NotNil(t, got, "backend never received a tools/call request")

	// REGRESSION GUARD (#5323): the upstream must see the FRESH token from
	// the per-request context, not the stale token captured at session-init.
	assert.Equal(t, "Bearer "+freshToken, got.Get("Authorization"),
		"per-request identity must drive the upstream Authorization header (#5323)")
	assert.NotEqual(t, "Bearer "+staleToken, got.Get("Authorization"),
		"captured session-init identity must not override per-request identity (#5323)")
}

// TestHTTPSession_FallbackIdentity_UsedWhenContextHasNoIdentity verifies the
// complementary invariant: when the per-request context carries no identity
// (e.g. mcp-go's Close() DELETE built from context.Background()), the captured
// session-init identity is used as a fallback so the teardown DELETE remains
// authenticated. This keeps the streamable-HTTP Close() path working without
// a 401-on-teardown regression.
func TestHTTPSession_FallbackIdentity_UsedWhenContextHasNoIdentity(t *testing.T) {
	t.Parallel()

	const (
		providerName     = "test-provider"
		fallbackTokenVal = "fallback-token"
	)

	fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
	url := newFakeBackend(t, fb)

	registry := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy(
		authtypes.StrategyTypeUpstreamInject,
		strategies.NewUpstreamInjectStrategy(),
	))

	target := &vmcp.BackendTarget{
		WorkloadID:    "identity-fallback-backend",
		WorkloadName:  "identity-fallback-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
		AuthConfig: &authtypes.BackendAuthStrategy{
			Type:           authtypes.StrategyTypeUpstreamInject,
			UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: providerName},
		},
	}

	fallbackIdentity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "user-1"},
		UpstreamTokens: map[string]string{providerName: fallbackTokenVal},
	}

	connector := NewHTTPConnector(registry)
	initCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	sess, _, err := connector(initCtx, target, fallbackIdentity, "", nil)
	require.NoError(t, err, "connector must initialise the backend successfully")
	t.Cleanup(func() { _ = sess.Close() })

	// CallTool with a bare context.Background() — no identity attached.
	// This models the teardown path where the SDK rebuilds the request
	// from a fresh background context.
	callCtx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancelCall)

	_, err = sess.CallTool(callCtx, "echo", map[string]any{}, nil)
	require.NoError(t, err, "tools/call must succeed via fallback identity")

	got := fb.headersFor(string(mcp.MethodToolsCall))
	require.NotNil(t, got, "backend never received a tools/call request")

	// The captured fallback must reach the upstream when the request
	// context has nothing to override it with.
	assert.Equal(t, "Bearer "+fallbackTokenVal, got.Get("Authorization"),
		"fallback identity must be injected when req.Context() carries none")
}
