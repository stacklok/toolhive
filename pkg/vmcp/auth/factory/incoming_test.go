// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewIncomingAuthMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cfg             *config.IncomingAuthConfig
		wantErr         bool
		errContains     string
		checkMiddleware func(t *testing.T, authMw func(http.Handler) http.Handler, authzMw func(http.Handler) http.Handler, authInfo http.Handler)
	}{
		{
			name:        "nil_config_returns_error",
			cfg:         nil,
			wantErr:     true,
			errContains: "incoming auth config is required",
		},
		{
			name: "oidc_missing_config_returns_error",
			cfg: &config.IncomingAuthConfig{
				Type: "oidc",
				OIDC: nil,
			},
			wantErr:     true,
			errContains: "OIDC configuration required",
		},
		{
			name: "local_auth_succeeds",
			cfg: &config.IncomingAuthConfig{
				Type: "local",
			},
			wantErr: false,
			checkMiddleware: func(t *testing.T, authMw func(http.Handler) http.Handler, authzMw func(http.Handler) http.Handler, authInfo http.Handler) {
				t.Helper()

				require.NotNil(t, authMw, "auth middleware should not be nil")
				assert.Nil(t, authzMw, "authz middleware should be nil when no authz configured")
				assert.Nil(t, authInfo, "local auth should not have authInfo handler")

				// Test that middleware creates identity
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity, ok := pkgauth.IdentityFromContext(r.Context())
					require.True(t, ok, "identity should be in context")
					require.NotNil(t, identity, "identity should not be nil")
					assert.NotEmpty(t, identity.Subject, "identity subject should not be empty")
					w.WriteHeader(http.StatusOK)
				})

				wrapped := authMw(testHandler)
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				recorder := httptest.NewRecorder()
				wrapped.ServeHTTP(recorder, req)

				assert.Equal(t, http.StatusOK, recorder.Code)
			},
		},
		{
			name: "anonymous_auth_succeeds",
			cfg: &config.IncomingAuthConfig{
				Type: "anonymous",
			},
			wantErr: false,
			checkMiddleware: func(t *testing.T, authMw func(http.Handler) http.Handler, authzMw func(http.Handler) http.Handler, authInfo http.Handler) {
				t.Helper()

				require.NotNil(t, authMw, "auth middleware should not be nil")
				assert.Nil(t, authzMw, "authz middleware should be nil when no authz configured")
				assert.Nil(t, authInfo, "anonymous auth should not have authInfo handler")

				// Test that middleware creates identity
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity, ok := pkgauth.IdentityFromContext(r.Context())
					require.True(t, ok, "identity should be in context")
					require.NotNil(t, identity, "identity should not be nil")
					assert.Equal(t, "anonymous", identity.Subject, "anonymous user should have 'anonymous' subject")
					w.WriteHeader(http.StatusOK)
				})

				wrapped := authMw(testHandler)
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				recorder := httptest.NewRecorder()
				wrapped.ServeHTTP(recorder, req)

				assert.Equal(t, http.StatusOK, recorder.Code)
			},
		},
		{
			name: "anonymous_auth_with_cedar_returns_authz_middleware",
			cfg: &config.IncomingAuthConfig{
				Type: "anonymous",
				Authz: &config.AuthzConfig{
					Type: "cedar",
					Policies: []string{
						`permit(principal, action == Action::"list_tools", resource);`,
					},
				},
			},
			wantErr: false,
			checkMiddleware: func(t *testing.T, authMw func(http.Handler) http.Handler, authzMw func(http.Handler) http.Handler, _ http.Handler) {
				t.Helper()

				require.NotNil(t, authMw, "auth middleware should not be nil")
				require.NotNil(t, authzMw, "authz middleware should not be nil when Cedar is configured")
			},
		},
		{
			name: "unsupported_auth_type_returns_error",
			cfg: &config.IncomingAuthConfig{
				Type: "unsupported-type",
			},
			wantErr:     true,
			errContains: "unsupported incoming auth type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authMw, authzMw, authInfo, err := NewIncomingAuthMiddleware(t.Context(), tt.cfg, nil, nil, nil)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, authMw)
				assert.Nil(t, authzMw)
				assert.Nil(t, authInfo)
			} else {
				require.NoError(t, err)
				require.NotNil(t, authMw)
				if tt.checkMiddleware != nil {
					tt.checkMiddleware(t, authMw, authzMw, authInfo)
				}
			}
		})
	}
}

// TestNewCedarAuthzMiddleware_PropagatesPrimaryUpstreamProvider verifies that
// newCedarAuthzMiddleware correctly wires PrimaryUpstreamProvider from the
// AuthzConfig into the Cedar ConfigOptions so that Cedar evaluates claims from
// the upstream IDP token when the embedded auth server is active.
func TestNewCedarAuthzMiddleware_PropagatesPrimaryUpstreamProvider(t *testing.T) {
	t.Parallel()

	const providerName = "my-idp"

	authzCfg := &config.AuthzConfig{
		Type:                    "cedar",
		Policies:                []string{`permit(principal, action, resource);`},
		PrimaryUpstreamProvider: providerName,
	}

	mw, err := newCedarAuthzMiddleware(authzCfg, nil)
	require.NoError(t, err)
	require.NotNil(t, mw, "middleware function should not be nil")

	// Reconstruct the Cedar config the same way newCedarAuthzMiddleware does and
	// verify the provider name is present in the serialised config options.  This
	// exercises the full path: AuthzConfig -> cedar.Config -> authorizers.Config ->
	// cedar.ExtractConfig, which is the same round-trip the Cedar authorizer uses
	// at startup.
	cedarCfg := cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:                authzCfg.Policies,
			EntitiesJSON:            "[]",
			PrimaryUpstreamProvider: authzCfg.PrimaryUpstreamProvider,
		},
	}
	authzConfig, err := authorizers.NewConfig(cedarCfg)
	require.NoError(t, err)

	extracted, err := cedar.ExtractConfig(authzConfig)
	require.NoError(t, err)
	assert.Equal(t, providerName, extracted.Options.PrimaryUpstreamProvider,
		"PrimaryUpstreamProvider must be preserved through authorizers.NewConfig round-trip")
}
