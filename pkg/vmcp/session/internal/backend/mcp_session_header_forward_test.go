// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestHTTPSession_AppliesHeaderForwardToPostInitializeRequests asserts the
// invariants of the HeaderForward transport stage on the session-side
// connector (pkg/vmcp/session/internal/backend). Fixes #5289.
//
// The three subtests cover, in order:
//   - plaintext: a user-configured AddPlaintextHeaders entry reaches the
//     backend on post-initialize traffic (tools/call).
//   - overlap precedence: when an inner auth stage writes the same header
//     name that HeaderForward configures, the inner stage's value lands on
//     the wire — proving the chain ordering documented on
//     headerForwardRoundTripper.RoundTrip.
//   - restricted-name rejection: NewHTTPConnector fails fast when
//     HeaderForward names a restricted header, proving the
//     resolveHeaderForward guard is wired into the session-side connector.
//
// Secret resolution requires t.Setenv and lives in its own non-parallel test
// below (TestHTTPSession_HeaderForward_ResolvesSecretsFromEnv).
func TestHTTPSession_AppliesHeaderForwardToPostInitializeRequests(t *testing.T) {
	t.Parallel()

	t.Run("plaintext header reaches backend on tools/call", func(t *testing.T) {
		t.Parallel()

		const (
			headerName  = "X-MCP-Toolsets"
			headerValue = "projects,issues,pull_requests,users,repos"
		)

		fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
		url := newFakeBackend(t, fb)

		target := &vmcp.BackendTarget{
			WorkloadID:    "header-forward-backend",
			WorkloadName:  "header-forward-backend",
			BaseURL:       url,
			TransportType: "streamable-http",
			HeaderForward: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					headerName: headerValue,
				},
			},
		}

		sess := connectAndCallEcho(t, target)
		t.Cleanup(func() { _ = sess.Close() })

		got := fb.headersFor(string(mcp.MethodToolsCall))
		require.NotNil(t, got, "backend never received a tools/call request")
		assert.Equal(t, headerValue, got.Get(headerName),
			"HeaderForward.AddPlaintextHeaders must reach the backend on post-initialize requests")
	})

	t.Run("inner auth stage wins on overlapping header name", func(t *testing.T) {
		t.Parallel()

		// The chain runs outer→inner on the outbound request:
		//   headerForwardRoundTripper → identityRoundTripper → authRoundTripper → http.DefaultTransport
		// headerForwardRoundTripper sets X-Test-Identity first (request didn't
		// have it). The inner authRoundTripper's Authenticate() then runs and
		// calls Set() unconditionally, overwriting. We assert the auth value
		// is the one on the wire.
		const (
			headerName        = "X-Test-Identity"
			headerForwardVal  = "from-header-forward"
			authStrategyValue = "from-auth"
		)

		fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
		url := newFakeBackend(t, fb)

		registry := vmcpauth.NewDefaultOutgoingAuthRegistry()
		require.NoError(t, registry.RegisterStrategy(
			"test-header-setter",
			&testHeaderSettingStrategy{name: "test-header-setter", header: headerName, value: authStrategyValue},
		))

		target := &vmcp.BackendTarget{
			WorkloadID:    "overlap-backend",
			WorkloadName:  "overlap-backend",
			BaseURL:       url,
			TransportType: "streamable-http",
			AuthConfig:    &authtypes.BackendAuthStrategy{Type: "test-header-setter"},
			HeaderForward: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					headerName: headerForwardVal,
				},
			},
		}

		connector := NewHTTPConnector(registry)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		sess, _, err := connector(ctx, target, nil, "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = sess.Close() })

		_, err = sess.CallTool(ctx, "echo", map[string]any{}, nil)
		require.NoError(t, err)

		got := fb.headersFor(string(mcp.MethodToolsCall))
		require.NotNil(t, got, "backend never received a tools/call request")
		assert.Equal(t, authStrategyValue, got.Get(headerName),
			"inner auth stage must overwrite HeaderForward on overlapping header names")
	})

	t.Run("restricted header in HeaderForward fails connector at startup", func(t *testing.T) {
		t.Parallel()

		// resolveHeaderForward rejects names in middleware.RestrictedHeaders.
		// Asserts the guard is reachable from the session-side connector — a
		// misconfigured backend surfaces at session creation, not silently as
		// a missing header on every request.
		fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
		url := newFakeBackend(t, fb)

		target := &vmcp.BackendTarget{
			WorkloadID:    "restricted-header-backend",
			WorkloadName:  "restricted-header-backend",
			BaseURL:       url,
			TransportType: "streamable-http",
			HeaderForward: &vmcp.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					// X-Forwarded-For is in middleware.RestrictedHeaders — letting
					// user config inject it would enable identity-spoofing of the
					// caller's IP to the backend.
					"X-Forwarded-For": "1.2.3.4",
				},
			},
		}

		registry := newTestRegistry(t)
		connector := NewHTTPConnector(registry)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, _, err := connector(ctx, target, nil, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restricted",
			"connector must reject restricted header names in HeaderForward config")
	})
}

// TestHTTPSession_HeaderForward_ResolvesSecretsFromEnv asserts that an
// AddHeadersFromSecret entry is resolved via the EnvironmentProvider that
// NewHTTPConnector constructs internally and reaches the backend on
// post-initialize traffic.
//
// Lives in its own non-parallel top-level test because t.Setenv (the only
// way to inject a value into the connector's env-backed secrets.Provider
// without changing production APIs) cannot be used inside a parallel test
// tree.
func TestHTTPSession_HeaderForward_ResolvesSecretsFromEnv(t *testing.T) {
	const (
		headerName    = "X-GitHub-Auth"
		secretID      = "GITHUB_PAT"
		secretEnvName = secrets.EnvVarPrefix + secretID
		secretValue   = "ghp_test_value_12345"
	)
	t.Setenv(secretEnvName, secretValue)

	fb := &fakeBackend{advertiseTools: true, tools: []mcp.Tool{{Name: "echo"}}}
	url := newFakeBackend(t, fb)

	target := &vmcp.BackendTarget{
		WorkloadID:    "secret-header-backend",
		WorkloadName:  "secret-header-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
		HeaderForward: &vmcp.HeaderForwardConfig{
			AddHeadersFromSecret: map[string]string{
				headerName: secretID,
			},
		},
	}

	sess := connectAndCallEcho(t, target)
	t.Cleanup(func() { _ = sess.Close() })

	got := fb.headersFor(string(mcp.MethodToolsCall))
	require.NotNil(t, got, "backend never received a tools/call request")
	assert.Equal(t, secretValue, got.Get(headerName),
		"HeaderForward.AddHeadersFromSecret must be resolved via the env provider and reach the backend")
}

// connectAndCallEcho builds an HTTP session for target via the default test
// registry, makes a single tools/call("echo"), and returns the open session.
// The caller is responsible for closing the session (typically via t.Cleanup).
func connectAndCallEcho(t *testing.T, target *vmcp.BackendTarget) Session {
	t.Helper()

	registry := newTestRegistry(t)
	connector := NewHTTPConnector(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	sess, caps, err := connector(ctx, target, nil, "")
	require.NoError(t, err, "connector must initialise the backend successfully")
	require.NotNil(t, sess, "connector returned nil session")
	require.NotNil(t, caps, "connector returned nil capability list")

	_, err = sess.CallTool(ctx, "echo", map[string]any{}, nil)
	require.NoError(t, err, "post-initialize CallTool must succeed")

	return sess
}

// testHeaderSettingStrategy is a vmcpauth.Strategy stand-in that unconditionally
// writes a single header onto every outbound request. Used to drive the
// overlap-precedence assertion: when HeaderForward configures the same name,
// this strategy (called from the inner authRoundTripper) must win on the wire.
type testHeaderSettingStrategy struct {
	name   string
	header string
	value  string
}

func (s *testHeaderSettingStrategy) Name() string { return s.name }

func (s *testHeaderSettingStrategy) Authenticate(_ context.Context, req *http.Request, _ *authtypes.BackendAuthStrategy) error {
	req.Header.Set(s.header, s.value)
	return nil
}

func (*testHeaderSettingStrategy) Validate(_ *authtypes.BackendAuthStrategy) error { return nil }
