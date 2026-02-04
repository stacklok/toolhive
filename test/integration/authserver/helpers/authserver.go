// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package helpers provides test utilities for auth server integration tests.
package helpers

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
)

// AuthServerOption is a functional option for configuring a test auth server.
type AuthServerOption func(*authServerConfig)

// authServerConfig holds configuration for creating a test auth server.
type authServerConfig struct {
	issuer           string
	upstreams        []authserver.UpstreamRunConfig
	allowedAudiences []string
	signingKeyConfig *authserver.SigningKeyRunConfig
	hmacSecretFiles  []string
	tokenLifespans   *authserver.TokenLifespanRunConfig
	scopesSupported  []string
}

// WithIssuer sets the issuer URL.
func WithIssuer(issuer string) AuthServerOption {
	return func(c *authServerConfig) {
		c.issuer = issuer
	}
}

// WithUpstreams sets the upstream IDP configurations.
func WithUpstreams(upstreams []authserver.UpstreamRunConfig) AuthServerOption {
	return func(c *authServerConfig) {
		c.upstreams = upstreams
	}
}

// WithAllowedAudiences sets the allowed resource audiences.
func WithAllowedAudiences(audiences []string) AuthServerOption {
	return func(c *authServerConfig) {
		c.allowedAudiences = audiences
	}
}

// WithSigningKey sets the signing key configuration.
func WithSigningKey(cfg *authserver.SigningKeyRunConfig) AuthServerOption {
	return func(c *authServerConfig) {
		c.signingKeyConfig = cfg
	}
}

// WithHMACSecrets sets the HMAC secret file paths.
func WithHMACSecrets(files []string) AuthServerOption {
	return func(c *authServerConfig) {
		c.hmacSecretFiles = files
	}
}

// WithTokenLifespans sets the token lifespan configuration.
func WithTokenLifespans(cfg *authserver.TokenLifespanRunConfig) AuthServerOption {
	return func(c *authServerConfig) {
		c.tokenLifespans = cfg
	}
}

// WithScopesSupported sets the supported scopes.
func WithScopesSupported(scopes []string) AuthServerOption {
	return func(c *authServerConfig) {
		c.scopesSupported = scopes
	}
}

// GetFreePort returns an available TCP port on localhost.
func GetFreePort(tb testing.TB) int {
	tb.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err, "failed to get free port")
	defer func() {
		_ = listener.Close()
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		tb.Fatalf("failed to get TCP address from listener")
	}
	return addr.Port
}

// NewTestAuthServerConfig creates a minimal valid RunConfig for testing.
// Uses development mode defaults (ephemeral signing keys, ephemeral HMAC secrets).
func NewTestAuthServerConfig(tb testing.TB, upstreamURL string, opts ...AuthServerOption) *authserver.RunConfig {
	tb.Helper()

	port := GetFreePort(tb)
	issuer := fmt.Sprintf("http://127.0.0.1:%d", port)

	cfg := &authServerConfig{
		issuer:           issuer,
		allowedAudiences: []string{"https://mcp.test.local"},
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// Build default upstream if not provided
	if len(cfg.upstreams) == 0 {
		cfg.upstreams = []authserver.UpstreamRunConfig{
			{
				Name: "test-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: upstreamURL + "/authorize",
					TokenEndpoint:         upstreamURL + "/token",
					ClientID:              "test-client-id",
					RedirectURI:           cfg.issuer + "/oauth/callback",
				},
			},
		}
	}

	return &authserver.RunConfig{
		SchemaVersion:    authserver.CurrentSchemaVersion,
		Issuer:           cfg.issuer,
		SigningKeyConfig: cfg.signingKeyConfig,
		HMACSecretFiles:  cfg.hmacSecretFiles,
		TokenLifespans:   cfg.tokenLifespans,
		Upstreams:        cfg.upstreams,
		ScopesSupported:  cfg.scopesSupported,
		AllowedAudiences: cfg.allowedAudiences,
	}
}

// NewEmbeddedAuthServer creates an embedded auth server for testing.
// Returns the server and handles cleanup on test completion.
func NewEmbeddedAuthServer(
	ctx context.Context,
	tb testing.TB,
	cfg *authserver.RunConfig,
) *authserverrunner.EmbeddedAuthServer {
	tb.Helper()

	server, err := authserverrunner.NewEmbeddedAuthServer(ctx, cfg)
	require.NoError(tb, err, "failed to create embedded auth server")

	tb.Cleanup(func() {
		_ = server.Close()
	})

	tb.Logf("Embedded auth server created with issuer: %s", cfg.Issuer)
	return server
}
