// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// Test constants for XAA tests.
const (
	testIDPTokenURL      = "https://idp.example.com/token"
	testTargetTokenURL   = "https://target.example.com/token"
	testTargetAudience   = "https://target.example.com"
	testTargetResource   = "https://mcp.example.com"
	testIDPClientID      = "idp-client"
	testTargetClientID   = "target-client"
	testProviderGitHub   = "github"
	testTargetClientName = "target-secret"
)

// createContextWithUpstreamIDTokens creates a context with an identity that has
// UpstreamIDTokens populated (used by the XAA strategy).
func createContextWithUpstreamIDTokens(idTokens map[string]string) context.Context {
	identity := &auth.Identity{
		PrincipalInfo:    auth.PrincipalInfo{Subject: "xaa-test-user"},
		Token:            "xaa-test-bearer",
		UpstreamIDTokens: idTokens,
	}
	return auth.WithIdentity(context.Background(), identity)
}

// createXAAStrategy builds a BackendAuthStrategy for xaa with the given options.
func createXAAStrategy(opts ...func(*authtypes.XAAConfig)) *authtypes.BackendAuthStrategy {
	cfg := &authtypes.XAAConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeXAA,
		XAA:  cfg,
	}
}

// createXAAIdPServer creates a mock IdP server that validates IdP exchange
// (RFC 8693) token exchange requests and returns an ID-JAG response.
func createXAAIdPServer(t *testing.T, expectedIDToken, idJAGToReturn string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		err := r.ParseForm()
		assert.NoError(t, err)

		// Verify RFC 8693 token exchange fields.
		assert.Equal(t, oauthproto.GrantTypeTokenExchange, r.Form.Get("grant_type"))
		assert.Equal(t, expectedIDToken, r.Form.Get("subject_token"))
		assert.Equal(t, oauthproto.TokenTypeIDToken, r.Form.Get("subject_token_type"))
		assert.Equal(t, oauthproto.TokenTypeIDJAG, r.Form.Get("requested_token_type"))
		assert.NotEmpty(t, r.Form.Get("audience"), "audience must be set")
		assert.NotEmpty(t, r.Form.Get("resource"), "resource must be set")

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(map[string]any{
			"access_token":      idJAGToReturn,
			"token_type":        "N_A",
			"issued_token_type": oauthproto.TokenTypeIDJAG,
			"expires_in":        300,
		})
		assert.NoError(t, err)
	}))
}

// createXAATargetServer creates a mock target AS server that validates target
// grant (RFC 7523 JWT Bearer grant) requests and returns a fixed access token.
func createXAATargetServer(t *testing.T, expectedAssertion string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		err := r.ParseForm()
		assert.NoError(t, err)

		// Verify JWT Bearer grant fields.
		assert.Equal(t, oauthproto.GrantTypeJWTBearer, r.Form.Get("grant_type"))
		assert.Equal(t, expectedAssertion, r.Form.Get("assertion"))

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "final-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
		assert.NoError(t, err)
	}))
}

// xaaStrategyWithResource builds a BackendAuthStrategy for xaa with the standard
// test config including TargetResource, pointing at the provided IdP/target URLs.
func xaaStrategyWithResource(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
	return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
		cfg.IDPTokenURL = idpURL
		cfg.TargetTokenURL = targetURL
		cfg.TargetAudience = testTargetAudience
		cfg.TargetResource = testTargetResource
		cfg.SubjectProviderName = testProviderGitHub
	})
}

// mustNotBeCalledTargetServer returns a mock target AS whose handler fails the
// test if invoked. Used by error-path cases where the IdP exchange must reject
// the assertion before the target grant runs.
func mustNotBeCalledTargetServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("target AS must not be called when IdP exchange rejects the ID-JAG")
	}))
}

func TestXAAStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		strategy    *authtypes.BackendAuthStrategy
		expectError string
	}{
		{
			name: "valid with all required fields",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "",
		},
		{
			name: "valid with all fields",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.IDPClientID = testIDPClientID
				cfg.IDPClientSecret = "idp-secret"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetClientID = testTargetClientID
				cfg.TargetClientSecret = testTargetClientName
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
				cfg.Scopes = []string{"read", "write"}
				cfg.SubjectProviderName = testProviderGitHub
			}),
			expectError: "",
		},
		{
			name:        "error on nil XAA config",
			strategy:    &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeXAA},
			expectError: "XAA configuration is required",
		},
		{
			name: "error on missing IDPTokenURL",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "IDPTokenURL is required",
		},
		{
			name: "error on missing TargetTokenURL",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetTokenURL is required",
		},
		{
			name: "error on missing TargetAudience",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetAudience is required",
		},
		{
			name: "valid without TargetResource",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
			}),
			expectError: "",
		},
		{
			name: "error on IdP secret without client ID",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.IDPClientSecret = "orphan-secret"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "client secret cannot be provided without client ID",
		},
		{
			name: "error on target secret without client ID",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetClientSecret = "orphan-secret"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "client secret cannot be provided without client ID",
		},
		{
			name: "error on IdP client ID without any secret source",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.IDPClientID = testIDPClientID
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "requires either client secret or client secret env var",
		},
		{
			name: "error on target client ID without any secret source",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetClientID = testTargetClientID
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "requires either client secret or client secret env var",
		},
		{
			name: "error on IDPTokenURL with http on non-localhost host",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "http://idp.example.com/token"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "IDPTokenURL",
		},
		{
			name: "error on TargetTokenURL with http on non-localhost host",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "http://target.example.com/token"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetTokenURL",
		},
		{
			name: "error on IDPTokenURL with non-http scheme",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "ftp://idp.example.com/token"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "IDPTokenURL",
		},
		{
			name: "error on TargetTokenURL with non-http scheme",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "ftp://target.example.com/token"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetTokenURL",
		},
		{
			name: "error on IDPTokenURL with fragment",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "https://idp.example.com/token#frag"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "IDPTokenURL must not contain a fragment",
		},
		{
			name: "error on TargetTokenURL with fragment",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "https://target.example.com/token#frag"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetTokenURL must not contain a fragment",
		},
		{
			name: "error on IDPTokenURL missing host",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "https:///token"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "IDPTokenURL must include a host",
		},
		{
			name: "error on TargetTokenURL missing host",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "https:///token"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "TargetTokenURL must include a host",
		},
		{
			name: "error on IDPTokenURL with embedded credentials",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "https://user:pass@idp.example.com/token"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "embedded credentials",
		},
		{
			name: "error on TargetTokenURL with embedded credentials",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "https://user:pass@target.example.com/token"
				cfg.TargetAudience = testTargetAudience
				cfg.TargetResource = testTargetResource
			}),
			expectError: "embedded credentials",
		},
		// InsecureTargetTokenURL=true disables the HTTPS requirement for TargetTokenURL entirely
		// (not just for localhost). Structural checks (fragment, credentials) still apply.
		{
			name: "InsecureTargetTokenURL=true allows http on localhost",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "http://localhost:8080/token"
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "",
		},
		{
			name: "InsecureTargetTokenURL=true allows http on any non-localhost host",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "http://target.example.com/token"
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "",
		},
		{
			name: "InsecureTargetTokenURL=true still rejects fragment in TargetTokenURL",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "http://localhost:8080/token#frag"
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "must not contain a fragment",
		},
		{
			name: "InsecureTargetTokenURL=true still rejects embedded credentials in TargetTokenURL",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "http://user:pass@localhost:8080/token"
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "embedded credentials",
		},
		{
			name: "InsecureTargetTokenURL=true still rejects non-http scheme in TargetTokenURL",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = testIDPTokenURL
				cfg.TargetTokenURL = "ftp://target.example.com/token"
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "TargetTokenURL must use http or https scheme",
		},
		{
			name: "InsecureTargetTokenURL=true does not relax IDPTokenURL validation",
			strategy: createXAAStrategy(func(cfg *authtypes.XAAConfig) {
				cfg.IDPTokenURL = "http://idp.example.com/token"
				cfg.TargetTokenURL = testTargetTokenURL
				cfg.TargetAudience = testTargetAudience
				cfg.InsecureTargetTokenURL = true
			}),
			expectError: "IDPTokenURL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockEnv := createMockEnvReader(t)
			s := NewXAAStrategy(mockEnv)
			err := s.Validate(tt.strategy)

			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestXAAStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		setupCtx         func() context.Context
		setupServers     func(t *testing.T) (idpServer *httptest.Server, targetServer *httptest.Server)
		strategy         func(idpURL, targetURL string) *authtypes.BackendAuthStrategy
		expectError      bool
		errorContains    []string
		errorNotContains []string // substrings that must be absent from err.Error()
		checkSentinel    bool
		checkAuthHeader  func(t *testing.T, req *http.Request)
	}{
		{
			name:     "health check without target client credentials skips authentication",
			setupCtx: func() context.Context { return healthcontext.WithHealthCheckMarker(context.Background()) },
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				dummy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("server should not be called when no target credentials are configured")
				}))
				return dummy, dummy
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Empty(t, req.Header.Get("Authorization"))
			},
		},
		{
			name:     "health check with target client credentials uses client credentials grant",
			setupCtx: func() context.Context { return healthcontext.WithHealthCheckMarker(context.Background()) },
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				// IdP should not be called during health check.
				idpServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("IdP should not be called for health check")
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					assert.NoError(t, r.ParseForm())
					assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
					clientID, clientSecret, ok := r.BasicAuth()
					assert.True(t, ok, "expected Basic Auth")
					assert.Equal(t, testTargetClientID, clientID)
					assert.Equal(t, testTargetClientName, clientSecret)

					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token": "health-check-token",
						"token_type":   "Bearer",
					})
					assert.NoError(t, err)
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetClientID = testTargetClientID
					cfg.TargetClientSecret = testTargetClientName
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer health-check-token", req.Header.Get("Authorization"))
			},
		},
		{
			name:     "health check client credentials error scrubs response body from err.Error()",
			setupCtx: func() context.Context { return healthcontext.WithHealthCheckMarker(context.Background()) },
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("IdP should not be called for health check")
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"unauthorized","tenant":"SENSITIVE-TENANT-ID"}`))
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetClientID = testTargetClientID
					cfg.TargetClientSecret = testTargetClientName
					cfg.TargetAudience = testTargetAudience
				})
			},
			expectError:      true,
			errorContains:    []string{"client credentials grant failed"},
			errorNotContains: []string{"SENSITIVE-TENANT-ID"},
		},
		{
			name: "returns error when no identity in context",
			setupCtx: func() context.Context {
				return context.Background()
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				dummy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("server should not be called")
				}))
				return dummy, dummy
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError:   true,
			errorContains: []string{"no identity found in context"},
		},
		{
			name: "returns ErrUpstreamTokenNotFound when provider ID token is missing",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(nil)
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				dummy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("server should not be called")
				}))
				return dummy, dummy
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"upstream token not found"},
			checkSentinel: true,
		},
		{
			name: "successful two-step flow",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := createXAATargetServer(t, idJAG)
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		{
			name: "IdP exchange fails with IdP error",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "bad-id-token"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					err := json.NewEncoder(w).Encode(map[string]any{
						"error":             "invalid_grant",
						"error_description": "subject token is invalid",
					})
					assert.NoError(t, err)
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("target AS should not be called when IdP exchange fails")
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"IdP exchange failed"},
		},
		{
			name: "target grant fails with target AS error",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "valid-id-token"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, testTargetResource)
				idpServer := createXAAIdPServer(t, "valid-id-token", idJAG)
				targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					err := json.NewEncoder(w).Encode(map[string]any{
						"error":             "invalid_grant",
						"error_description": "assertion is invalid",
					})
					assert.NoError(t, err)
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"target grant failed"},
		},
		{
			name: "IdP exchange rejects wrong issued_token_type",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "not-an-id-jag",
						"token_type":        "Bearer",
						"issued_token_type": oauthproto.TokenTypeAccessToken,
						"expires_in":        300,
					})
					assert.NoError(t, err)
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("target AS must not be called when IdP exchange returns the wrong issued_token_type")
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"IdP exchange", "issued_token_type", oauthproto.TokenTypeIDJAG},
		},
		{
			name: "IdP exchange rejects wrong token_type",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "some-token",
						"token_type":        "Bearer",
						"issued_token_type": oauthproto.TokenTypeIDJAG,
						"expires_in":        300,
					})
					assert.NoError(t, err)
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("target AS must not be called when IdP exchange returns the wrong token_type")
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"N_A"},
		},
		{
			name: "IdP exchange rejects empty access_token",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "",
						"token_type":        "N_A",
						"issued_token_type": oauthproto.TokenTypeIDJAG,
						"expires_in":        300,
					})
					assert.NoError(t, err)
				}))
				targetServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("target AS must not be called when IdP exchange returns an empty assertion")
				}))
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError:   true,
			errorContains: []string{"IdP exchange", "access_token"},
		},
		{
			name: "IdP exchange typ header mismatch is not fatal",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				wrongTypJWT := buildIDJAGJWT(t, "JWT", testTargetAudience, testTargetResource)
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token":      wrongTypJWT,
						"token_type":        "N_A",
						"issued_token_type": oauthproto.TokenTypeIDJAG,
						"expires_in":        300,
					})
					assert.NoError(t, err)
				}))
				targetServer := createXAATargetServer(t, wrongTypJWT)
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		{
			name: "IdP exchange typ header matches oauth-id-jag+jwt",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				correctTypJWT := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, testTargetResource)
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					err := json.NewEncoder(w).Encode(map[string]any{
						"access_token":      correctTypJWT,
						"token_type":        "N_A",
						"issued_token_type": oauthproto.TokenTypeIDJAG,
						"expires_in":        300,
					})
					assert.NoError(t, err)
				}))
				targetServer := createXAATargetServer(t, correctTypJWT)
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		// aud claim validation (draft §3.1: aud is REQUIRED).
		{
			name: "IdP exchange rejects ID-JAG with missing aud claim",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", nil, testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "aud"},
		},
		{
			name: "IdP exchange rejects ID-JAG with wrong aud (string)",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", "https://wrong.example.com", testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "aud"},
		},
		{
			name: "IdP exchange rejects ID-JAG with wrong aud (array)",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					[]string{"https://wrong.example.com", "https://other.example.com"}, testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "aud"},
		},
		{
			name: "IdP exchange rejects ID-JAG with multi-element aud array",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					[]string{testTargetAudience, "https://extra.example.com"}, testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "aud"},
		},
		{
			name: "IdP exchange accepts ID-JAG with aud as single-element array",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					[]string{testTargetAudience}, testTargetResource)
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := createXAATargetServer(t, idJAG)
				return idpServer, targetServer
			},
			strategy:    xaaStrategyWithResource,
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		// resource claim validation (draft §3.1: resource is OPTIONAL, validated
		// when one was requested).
		{
			name: "IdP exchange rejects ID-JAG with wrong resource",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, "https://wrong.example.com")
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "resource"},
		},
		{
			name: "IdP exchange rejects ID-JAG with missing resource when requested",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				// resource omitted (empty string) while config requests one.
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, "")
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "resource"},
		},
		{
			name: "IdP exchange accepts ID-JAG with resource as array containing TargetResource",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					testTargetAudience, []string{testTargetResource})
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := createXAATargetServer(t, idJAG)
				return idpServer, targetServer
			},
			strategy:    xaaStrategyWithResource,
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		{
			name: "IdP exchange accepts ID-JAG with resource array containing a non-string element",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					testTargetAudience, []interface{}{float64(123), testTargetResource})
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := createXAATargetServer(t, idJAG)
				return idpServer, targetServer
			},
			strategy:    xaaStrategyWithResource,
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		{
			name: "IdP exchange rejects ID-JAG with resource array containing only a non-string element",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt",
					testTargetAudience, []interface{}{float64(123)})
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", idJAG)
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "resource"},
		},
		// resource check skipped when TargetResource is not configured
		// (wantResource == "" early-return branch in validateIDJAGClaims).
		{
			name: "IdP exchange accepts ID-JAG when TargetResource is not configured",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idJAG := buildIDJAGJWT(t, "oauth-id-jag+jwt", testTargetAudience, nil)
				// TargetResource is unset, so the exchange request carries no
				// resource parameter; use an IdP mock that does not assert it.
				idpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					assert.Equal(t, http.MethodPost, r.Method)
					require.NoError(t, r.ParseForm())
					assert.Equal(t, oauthproto.GrantTypeTokenExchange, r.Form.Get("grant_type"))
					assert.Equal(t, "user-id-token-jwt", r.Form.Get("subject_token"))
					assert.Equal(t, oauthproto.TokenTypeIDJAG, r.Form.Get("requested_token_type"))
					assert.NotEmpty(t, r.Form.Get("audience"), "audience must be set")
					assert.Empty(t, r.Form.Get("resource"), "resource must be absent when not configured")

					w.Header().Set("Content-Type", "application/json")
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
						"access_token":      idJAG,
						"token_type":        "N_A",
						"issued_token_type": oauthproto.TokenTypeIDJAG,
						"expires_in":        300,
					}))
				}))
				targetServer := createXAATargetServer(t, idJAG)
				return idpServer, targetServer
			},
			strategy: func(idpURL, targetURL string) *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = idpURL
					cfg.TargetTokenURL = targetURL
					cfg.TargetAudience = testTargetAudience
					// TargetResource intentionally omitted.
					cfg.SubjectProviderName = testProviderGitHub
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer final-access-token", req.Header.Get("Authorization"))
			},
		},
		// parse failure: a non-JWT assertion cannot be a valid ID-JAG.
		{
			name: "IdP exchange rejects non-JWT assertion",
			setupCtx: func() context.Context {
				return createContextWithUpstreamIDTokens(
					map[string]string{testProviderGitHub: "user-id-token-jwt"})
			},
			setupServers: func(t *testing.T) (*httptest.Server, *httptest.Server) {
				t.Helper()
				idpServer := createXAAIdPServer(t, "user-id-token-jwt", "not-a-jwt")
				targetServer := mustNotBeCalledTargetServer(t)
				return idpServer, targetServer
			},
			strategy:      xaaStrategyWithResource,
			expectError:   true,
			errorContains: []string{"IdP exchange", "not a valid JWT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			idpServer, targetServer := tt.setupServers(t)
			t.Cleanup(idpServer.Close)
			t.Cleanup(targetServer.Close)

			mockEnv := createMockEnvReader(t)
			s := NewXAAStrategy(mockEnv)
			ctx := tt.setupCtx()

			backendStrategy := tt.strategy(idpServer.URL, targetServer.URL)
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			err := s.Authenticate(ctx, req, backendStrategy)

			if tt.expectError {
				require.Error(t, err)
				for _, substr := range tt.errorContains {
					assert.Contains(t, err.Error(), substr)
				}
				for _, substr := range tt.errorNotContains {
					assert.NotContains(t, err.Error(), substr)
				}
				if tt.checkSentinel {
					assert.True(t, errors.Is(err, authtypes.ErrUpstreamTokenNotFound),
						"expected error to wrap ErrUpstreamTokenNotFound, got: %v", err)
				}
				return
			}

			require.NoError(t, err)
			if tt.checkAuthHeader != nil {
				tt.checkAuthHeader(t, req)
			}
		})
	}
}

// testJWTSigningKey is a throwaway RSA key shared by every buildIDJAGJWT call
// in this file. Tokens built with it are only ever consumed via
// ParseUnverified (signature never checked), so a single package-level key
// avoids generating a fresh RSA-2048 key per test case.
var testJWTSigningKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

// buildIDJAGJWT signs a JWT with the given typ header, aud claim, and resource
// claim using testJWTSigningKey. aud and resource may each be a string,
// []string (to exercise the array form), or []interface{} (to exercise
// heterogeneous arrays); a nil or empty string omits the claim. The token is
// not intended to be verified — it exists to exercise the claim-validation
// path in performIDPExchange.
func buildIDJAGJWT(t *testing.T, typ string, aud, resource any) string {
	t.Helper()

	claims := jwt.MapClaims{"sub": "test"}
	setClaim(t, claims, "aud", aud)
	setClaim(t, claims, "resource", resource)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["typ"] = typ

	signed, err := token.SignedString(testJWTSigningKey)
	require.NoError(t, err)
	return signed
}

// setClaim sets a string-or-array claim on a MapClaims, omitting it when v is
// nil or an empty string. []string is marshalled as a JSON array (decoded back
// by golang-jwt as []interface{}, exercising the array branch of
// claimContainsString). []interface{} is set directly, allowing heterogeneous
// arrays (e.g. mixing strings with non-string elements) to exercise the
// type-guard in claimContainsString's array branch.
func setClaim(t *testing.T, claims jwt.MapClaims, key string, v any) {
	t.Helper()
	switch c := v.(type) {
	case nil:
		// omit
	case string:
		if c == "" {
			return
		}
		claims[key] = c
	case []string:
		claims[key] = c
	case []interface{}:
		claims[key] = c
	default:
		t.Fatalf("unsupported %s type %T", key, v)
	}
}

func TestXAAStrategy_ClientSecretEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupMock      func(t *testing.T, mockEnv *mocks.MockReader)
		strategyConfig func() *authtypes.BackendAuthStrategy
		expectError    bool
		errorContains  string
	}{
		{
			name: "resolves IdP client secret from env",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("IDP_SECRET_ENV").Return("idp-secret-from-env").AnyTimes()
			},
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.IDPClientID = testIDPClientID
					cfg.IDPClientSecretEnv = "IDP_SECRET_ENV"
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError: false,
		},
		{
			name: "resolves target client secret from env",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("TARGET_SECRET_ENV").Return("target-secret-from-env").AnyTimes()
			},
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetClientID = testTargetClientID
					cfg.TargetClientSecretEnv = "TARGET_SECRET_ENV"
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError: false,
		},
		{
			name: "error when IdP env var is empty",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("MISSING_IDP_SECRET").Return("").AnyTimes()
			},
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.IDPClientID = testIDPClientID
					cfg.IDPClientSecretEnv = "MISSING_IDP_SECRET"
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError:   true,
			errorContains: "environment variable MISSING_IDP_SECRET not set or empty",
		},
		{
			name: "error when target env var is empty",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("MISSING_TARGET_SECRET").Return("").AnyTimes()
			},
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetClientID = testTargetClientID
					cfg.TargetClientSecretEnv = "MISSING_TARGET_SECRET"
					cfg.TargetAudience = testTargetAudience
					cfg.TargetResource = testTargetResource
				})
			},
			expectError:   true,
			errorContains: "environment variable MISSING_TARGET_SECRET not set or empty",
		},
		{
			// clientID=="" check fires before Getenv is called; no mock expectation needed.
			name:      "error when IdP secret env set without IdP client ID",
			setupMock: nil,
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.IDPClientSecretEnv = "SOME_IDP_SECRET_ENV"
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetAudience = testTargetAudience
				})
			},
			expectError:   true,
			errorContains: "client secret env cannot be provided without client ID",
		},
		{
			// clientID=="" check fires before Getenv is called; no mock expectation needed.
			name:      "error when target secret env set without target client ID",
			setupMock: nil,
			strategyConfig: func() *authtypes.BackendAuthStrategy {
				return createXAAStrategy(func(cfg *authtypes.XAAConfig) {
					cfg.IDPTokenURL = testIDPTokenURL
					cfg.TargetTokenURL = testTargetTokenURL
					cfg.TargetClientSecretEnv = "SOME_TARGET_SECRET_ENV"
					cfg.TargetAudience = testTargetAudience
				})
			},
			expectError:   true,
			errorContains: "client secret env cannot be provided without client ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)
			if tt.setupMock != nil {
				tt.setupMock(t, mockEnv)
			}

			s := NewXAAStrategy(mockEnv)
			err := s.Validate(tt.strategyConfig())

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
