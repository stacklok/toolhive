// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const unregisteredActorValidatorError = `delegate_clients: client_id "delegate": ` +
	`actor_profile.validator "broker" is not registered in ActorTokenValidators`

func actorProfileRunnerConfig(t *testing.T) *authserver.RunConfig {
	t.Helper()
	secretFile := filepath.Join(t.TempDir(), "delegate-secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("s3cr3t-value-that-is-long-enough-for-32-chars"), 0o600))

	cfg := minimalRunnerConsentConfig("https://actor-auth.example.com")
	cfg.ScopesSupported = []string{"openid", "offline_access"}
	cfg.AllowedAudiences = []string{"https://mcp.example.com", "https://input.example.com"}
	cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
		TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
			DelegateClients: []authserver.DelegateClientRunConfig{{
				ClientID:         "delegate",
				ClientSecretFile: secretFile,
				Scopes:           []string{"openid"},
				Audiences:        []string{"https://mcp.example.com"},
				TokenExchange: &authserver.DelegateClientTokenExchange{
					SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
					AllowedSourceClients:    []string{"tui"},
					SubjectTokenAudiences:   []string{"https://input.example.com"},
					ExchangeRefreshTokens:   &authserver.DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "12h"},
					IDJAG: &authserver.DelegateClientIDJAG{Targets: []authserver.DelegateClientIDJAGTarget{{
						Issuer:    "https://as2.example.com",
						ClientID:  "broker-at-as2",
						Resources: []string{"https://gw.example.com/mcp"},
					}}},
					ActorProfile: &authserver.DelegateClientActorProfile{
						Validator:      "broker",
						ActorTokenType: oauthproto.TokenTypeJWT,
					},
				},
			}},
		},
	}
	return cfg
}

func TestNewEmbeddedAuthServerWithStorage_ActorProfiles(t *testing.T) {
	t.Parallel()

	t.Run("matching validator starts the server", func(t *testing.T) {
		t.Parallel()

		validators := map[string]tokenexchange.ActorValidatorRegistration{
			"broker": {Validator: mocks.NewMockActorTokenValidator(gomock.NewController(t)), ActIssuer: "spiffe://example.org"},
		}
		srv, err := NewEmbeddedAuthServerWithStorage(
			context.Background(), actorProfileRunnerConfig(t),
			storage.NewMemoryStorage(), WithActorTokenValidators(validators))
		require.NoError(t, err)
		t.Cleanup(func() { _ = srv.Close() })
	})

	// The upstream uses DCR discovery against a counting server, so any
	// upstream resolution before the profile check would show up as a request.
	tests := []struct {
		name    string
		options func(t *testing.T) []EmbeddedAuthServerOption
		wantErr string
	}{
		{
			name: "option map lacks the name",
			options: func(t *testing.T) []EmbeddedAuthServerOption {
				t.Helper()
				return []EmbeddedAuthServerOption{WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
					"other": {Validator: mocks.NewMockActorTokenValidator(gomock.NewController(t)), ActIssuer: "spiffe://example.org"},
				})}
			},
			wantErr: unregisteredActorValidatorError,
		},
		{
			name:    "no option",
			options: func(*testing.T) []EmbeddedAuthServerOption { return nil },
			wantErr: unregisteredActorValidatorError,
		},
		{
			name: "nil validator under the name",
			options: func(*testing.T) []EmbeddedAuthServerOption {
				return []EmbeddedAuthServerOption{WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
					"broker": {ActIssuer: "spiffe://example.org"},
				})}
			},
			wantErr: `actor token validator "broker" is nil`,
		},
		{
			name: "ActIssuer is the server issuer",
			options: func(t *testing.T) []EmbeddedAuthServerOption {
				t.Helper()
				return []EmbeddedAuthServerOption{WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
					"broker": {
						Validator: mocks.NewMockActorTokenValidator(gomock.NewController(t)),
						ActIssuer: "https://actor-auth.example.com",
					},
				})}
			},
			wantErr: `actor token validator "broker": ActIssuer must not be this server's issuer`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+" fails before upstream work", func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(upstream.Close)

			cfg := actorProfileRunnerConfig(t)
			cfg.Upstreams = dcrDiscoveryUpstreams(upstream.URL)

			tracker := &closeTrackingStorage{Storage: storage.NewMemoryStorage()}
			_, err := NewEmbeddedAuthServerWithStorage(context.Background(), cfg, tracker, tt.options(t)...)
			require.EqualError(t, err, tt.wantErr)
			assert.Zero(t, requests.Load(), "the profile check must run before discovery and DCR")
			assert.Zero(t, tracker.closeCount.Load(), "supplied storage is borrowed and stays open on early failure")
		})
	}
}

func TestNewEmbeddedAuthServer_ActorProfilesFailBeforeStorageCreation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		options   func(t *testing.T) []EmbeddedAuthServerOption
		wantError string
	}{
		{
			name:      "validator not registered",
			options:   func(*testing.T) []EmbeddedAuthServerOption { return nil },
			wantError: unregisteredActorValidatorError,
		},
		{
			name: "validator registered reaches createStorage",
			options: func(t *testing.T) []EmbeddedAuthServerOption {
				t.Helper()
				return []EmbeddedAuthServerOption{WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
					"broker": {Validator: mocks.NewMockActorTokenValidator(gomock.NewController(t)), ActIssuer: "spiffe://example.org"},
				})}
			},
			wantError: "failed to create storage",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := actorProfileRunnerConfig(t)
			cfg.Storage = &storage.RunConfig{Type: "unknown-backend"}
			_, err := NewEmbeddedAuthServer(context.Background(), cfg, tt.options(t)...)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestWithActorTokenValidators_CopiesInput(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	first := mocks.NewMockActorTokenValidator(ctrl)
	firstReg := tokenexchange.ActorValidatorRegistration{Validator: first, ActIssuer: "spiffe://example.org"}
	input := map[string]tokenexchange.ActorValidatorRegistration{"broker": firstReg}
	option := WithActorTokenValidators(input)

	input["injected"] = tokenexchange.ActorValidatorRegistration{Validator: mocks.NewMockActorTokenValidator(ctrl)}
	delete(input, "broker")

	var options embeddedAuthServerOptions
	option(&options)
	require.Len(t, options.actorTokenValidators, 1)
	assert.Same(t, first, options.actorTokenValidators["broker"].Validator)
	assert.Equal(t, "spiffe://example.org", options.actorTokenValidators["broker"].ActIssuer)

	options.actorTokenValidators["mutated"] = firstReg
	var again embeddedAuthServerOptions
	option(&again)
	assert.Len(t, again.actorTokenValidators, 1, "applying the option hands out a fresh copy each time")

	replacement := map[string]tokenexchange.ActorValidatorRegistration{"other": firstReg}
	options = resolveEmbeddedAuthServerOptions([]EmbeddedAuthServerOption{option, WithActorTokenValidators(replacement)})
	assert.Len(t, options.actorTokenValidators, 1)
	assert.Contains(t, options.actorTokenValidators, "other", "repeated options replace the map")
}

func dcrDiscoveryUpstreams(baseURL string) []authserver.UpstreamRunConfig {
	return []authserver.UpstreamRunConfig{{
		Name: "dcr-upstream",
		Type: authserver.UpstreamProviderTypeOAuth2,
		OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
			AuthorizationEndpoint: baseURL + "/authorize",
			TokenEndpoint:         baseURL + "/token",
			DCRConfig: &authserver.DCRUpstreamConfig{
				DiscoveryURL: baseURL + "/.well-known/oauth-authorization-server",
			},
		},
	}}
}
