// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// stubRegistry is an in-process BackendRegistry used only in tests.
type stubRegistry struct {
	backends []vmcp.Backend
}

func (s *stubRegistry) Get(_ context.Context, id string) *vmcp.Backend {
	for i := range s.backends {
		if s.backends[i].ID == id {
			b := s.backends[i]
			return &b
		}
	}
	return nil
}

func (s *stubRegistry) List(_ context.Context) []vmcp.Backend {
	out := make([]vmcp.Backend, len(s.backends))
	copy(out, s.backends)
	return out
}

func (s *stubRegistry) Count() int { return len(s.backends) }

// requestWithIdentity builds a test *http.Request carrying the given identity.
func requestWithIdentity(identity *auth.Identity) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	ctx := auth.WithIdentity(r.Context(), identity)
	return r.WithContext(ctx)
}

// identityWithTokens constructs an auth.Identity whose UpstreamTokens map
// contains the supplied provider→token entries.
func identityWithTokens(tokens map[string]string) *auth.Identity {
	return &auth.Identity{UpstreamTokens: tokens}
}

func TestUpstreamProviderName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cfg      *authtypes.BackendAuthStrategy
		expected string
	}{
		{
			name:     "nil config returns empty",
			cfg:      nil,
			expected: "",
		},
		{
			name:     "unauthenticated returns empty",
			cfg:      &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated},
			expected: "",
		},
		{
			name:     "header_injection returns empty",
			cfg:      &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeHeaderInjection},
			expected: "",
		},
		{
			name: "upstream_inject returns ProviderName",
			cfg: &authtypes.BackendAuthStrategy{
				Type:           authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"},
			},
			expected: "github",
		},
		{
			name: "upstream_inject with nil sub-config returns empty",
			cfg: &authtypes.BackendAuthStrategy{
				Type:           authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: nil,
			},
			expected: "",
		},
		{
			name: "token_exchange with SubjectProviderName returns it",
			cfg: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:            "https://token.example.com",
					SubjectProviderName: "google",
				},
			},
			expected: "google",
		},
		{
			name: "token_exchange without SubjectProviderName returns empty",
			cfg: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://token.example.com",
				},
			},
			expected: "",
		},
		{
			name: "aws_sts with SubjectProviderName returns it",
			cfg: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeAwsSts,
				AwsSts: &authtypes.AwsStsConfig{
					Region:              "us-east-1",
					SubjectProviderName: "aws-idp",
				},
			},
			expected: "aws-idp",
		},
		{
			name: "obo with SubjectTokenProviderName returns it",
			cfg: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeOBO,
				OBO: &authtypes.OBOConfig{
					TokenURL:                 "https://login.example.com",
					SubjectTokenProviderName: "entra",
				},
			},
			expected: "entra",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, upstreamProviderName(tc.cfg))
		})
	}
}

func TestUpstreamTokenCheckMiddleware(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name        string
		backends    []vmcp.Backend
		request     *http.Request
		wantStatus  int
		wantWWWAuth bool
	}{
		{
			name:       "no identity passes through",
			backends:   []vmcp.Backend{{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"}}}},
			request:    httptest.NewRequest(http.MethodPost, "/mcp", nil),
			wantStatus: http.StatusOK,
		},
		{
			name:       "no backends with upstream requirements passes through",
			backends:   []vmcp.Backend{{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated}}},
			request:    requestWithIdentity(identityWithTokens(nil)),
			wantStatus: http.StatusOK,
		},
		{
			name: "all required tokens present passes through",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"}}},
			},
			request:    requestWithIdentity(identityWithTokens(map[string]string{"github": "ghp_abc123"})),
			wantStatus: http.StatusOK,
		},
		{
			name: "missing required upstream token returns 401 with WWW-Authenticate",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"}}},
			},
			request:     requestWithIdentity(identityWithTokens(map[string]string{})),
			wantStatus:  http.StatusUnauthorized,
			wantWWWAuth: true,
		},
		{
			name: "one of two tokens missing returns 401",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"}}},
				{ID: "b2", AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject, UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "gitlab"}}},
			},
			// github token present, gitlab token missing
			request:     requestWithIdentity(identityWithTokens(map[string]string{"github": "ghp_abc123"})),
			wantStatus:  http.StatusUnauthorized,
			wantWWWAuth: true,
		},
		{
			name:       "empty registry passes through",
			backends:   []vmcp.Backend{},
			request:    requestWithIdentity(identityWithTokens(nil)),
			wantStatus: http.StatusOK,
		},
		{
			name: "nil AuthConfig on backend is ignored",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: nil},
			},
			request:    requestWithIdentity(identityWithTokens(nil)),
			wantStatus: http.StatusOK,
		},
		{
			name: "token_exchange with SubjectProviderName - token present passes",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeTokenExchange,
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL:            "https://token.example.com",
						SubjectProviderName: "google",
					},
				}},
			},
			request:    requestWithIdentity(identityWithTokens(map[string]string{"google": "ya29.tok"})),
			wantStatus: http.StatusOK,
		},
		{
			name: "token_exchange with SubjectProviderName - token missing returns 401",
			backends: []vmcp.Backend{
				{ID: "b1", AuthConfig: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeTokenExchange,
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL:            "https://token.example.com",
						SubjectProviderName: "google",
					},
				}},
			},
			request:     requestWithIdentity(identityWithTokens(map[string]string{})),
			wantStatus:  http.StatusUnauthorized,
			wantWWWAuth: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := &stubRegistry{backends: tc.backends}
			handler := upstreamTokenCheckMiddleware(registry)(ok)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, tc.request)

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantWWWAuth {
				wwwAuth := rec.Header().Get("WWW-Authenticate")
				assert.NotEmpty(t, wwwAuth, "expected WWW-Authenticate header")
				assert.Contains(t, wwwAuth, `error="invalid_token"`)
			}
		})
	}
}
