// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// twoUpstreamChain is the effective chain for multiUpstreamTestSetup's two
// configured providers, in configured order.
var twoUpstreamChain = []string{"provider-1", "provider-2"}

// stubUpstreamFilter is a hand-written test double for UpstreamFilter, mirroring
// the mockIDPProvider pattern used elsewhere in this package. It records how many
// times it was called and the arguments it was passed (the non-first upstream
// names and the resolved principal), and returns a canned keep set or error.
type stubUpstreamFilter struct {
	keep              []string
	err               error
	calls             int
	capturedArgs      []string
	capturedPrincipal auth.PrincipalInfo
}

func (f *stubUpstreamFilter) FilterUpstreams(
	_ context.Context,
	principal auth.PrincipalInfo,
	configured []string,
) ([]string, error) {
	f.calls++
	f.capturedArgs = configured
	f.capturedPrincipal = principal
	if f.err != nil {
		return nil, f.err
	}
	return f.keep, nil
}

// newChainTestHandler builds a Handler over upstreams named after the given
// slice (each backed by a throwaway OAuth2 mock provider), forwarding any
// Options. It exists so chain-resolution tests can control the exact number and
// order of configured upstreams.
func newChainTestHandler(t *testing.T, names []string, opts ...Option) *Handler {
	t.Helper()

	provider, oauth2Config, stor, _ := baseTestSetup(t)
	upstreams := make([]NamedUpstream, len(names))
	for i, n := range names {
		upstreams[i] = NamedUpstream{Name: n, Provider: &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}}
	}
	handler, err := NewHandler(provider, oauth2Config, stor, upstreams, opts...)
	require.NoError(t, err)
	return handler
}

func TestComputeChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		upstreams    []string
		filter       *stubUpstreamFilter
		want         []string
		wantErr      bool
		wantCalls    int
		wantFilterIn []string
	}{
		{
			name:      "no filter walks all configured upstreams in order",
			upstreams: []string{"provider-1", "provider-2", "provider-3"},
			filter:    nil,
			want:      []string{"provider-1", "provider-2", "provider-3"},
		},
		{
			name:      "single upstream does not consult filter",
			upstreams: []string{"provider-1"},
			filter:    &stubUpstreamFilter{keep: []string{"anything"}},
			want:      []string{"provider-1"},
			wantCalls: 0,
		},
		{
			name:         "filter keeps a subset, first upstream always leads",
			upstreams:    []string{"provider-1", "provider-2", "provider-3"},
			filter:       &stubUpstreamFilter{keep: []string{"provider-3"}},
			want:         []string{"provider-1", "provider-3"},
			wantCalls:    1,
			wantFilterIn: []string{"provider-2", "provider-3"},
		},
		{
			name:         "filter keeps none leaves only the first upstream",
			upstreams:    []string{"provider-1", "provider-2", "provider-3"},
			filter:       &stubUpstreamFilter{keep: []string{}},
			want:         []string{"provider-1"},
			wantCalls:    1,
			wantFilterIn: []string{"provider-2", "provider-3"},
		},
		{
			name:      "returned order is ignored, configured order is preserved",
			upstreams: []string{"provider-1", "provider-2", "provider-3"},
			filter:    &stubUpstreamFilter{keep: []string{"provider-3", "provider-2"}},
			want:      []string{"provider-1", "provider-2", "provider-3"},
			wantCalls: 1,
		},
		{
			name:      "unknown and first-upstream names in keep set are ignored",
			upstreams: []string{"provider-1", "provider-2"},
			filter:    &stubUpstreamFilter{keep: []string{"provider-1", "does-not-exist", "provider-2"}},
			want:      []string{"provider-1", "provider-2"},
			wantCalls: 1,
		},
		{
			name:      "filter error is propagated, no fallback to full walk",
			upstreams: []string{"provider-1", "provider-2"},
			filter:    &stubUpstreamFilter{err: errors.New("filter boom")},
			wantErr:   true,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []Option
			if tt.filter != nil {
				opts = append(opts, WithUpstreamFilter(tt.filter))
			}
			handler := newChainTestHandler(t, tt.upstreams, opts...)

			principal := auth.PrincipalInfo{
				PlatformUserID: "canonical-user-1",
				Subject:        "upstream-sub-1",
				Email:          "user@example.com",
				Claims:         map[string]any{"groups": []any{"admins"}},
			}

			got, err := handler.computeChain(context.Background(), principal)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			if tt.filter != nil {
				assert.Equal(t, tt.wantCalls, tt.filter.calls, "filter call count")
				if tt.wantFilterIn != nil {
					assert.Equal(t, tt.wantFilterIn, tt.filter.capturedArgs,
						"filter must receive non-first configured upstreams in order")
				}
				if tt.wantCalls > 0 {
					assert.Equal(t, principal, tt.filter.capturedPrincipal,
						"filter must receive the resolved principal (platform user, subject + claims) verbatim")
				}
			}
		})
	}
}

func TestValidateChain(t *testing.T) {
	t.Parallel()

	// Configured order is provider-1, provider-2, provider-3.
	handler := newChainTestHandler(t, []string{"provider-1", "provider-2", "provider-3"})

	tests := []struct {
		name    string
		chain   []string
		wantErr string // substring to match; empty means no error expected
	}{
		{name: "full configured chain", chain: []string{"provider-1", "provider-2", "provider-3"}},
		{name: "in-order subsequence", chain: []string{"provider-1", "provider-3"}},
		{name: "first upstream only", chain: []string{"provider-1"}},
		{name: "empty chain", chain: nil, wantErr: "chain is empty"},
		{
			name: "wrong first upstream", chain: []string{"provider-2", "provider-3"},
			wantErr: "must lead with the first configured upstream",
		},
		{
			name: "unconfigured entry", chain: []string{"provider-1", "provider-x"},
			wantErr: "unconfigured, out of order, or duplicated",
		},
		{
			name: "out of order", chain: []string{"provider-1", "provider-3", "provider-2"},
			wantErr: "unconfigured, out of order, or duplicated",
		},
		{
			name: "duplicate first upstream", chain: []string{"provider-1", "provider-1"},
			wantErr: "unconfigured, out of order, or duplicated",
		},
		{
			name: "duplicate non-first upstream", chain: []string{"provider-1", "provider-2", "provider-2"},
			wantErr: "unconfigured, out of order, or duplicated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := handler.validateChain(tt.chain)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

// TestNextMissingUpstream_RespectsChainSubset verifies that a leg absent from the
// chain is never reported missing even when it has no stored token — the filter
// dropped it, so the walk must not prompt for it.
func TestNextMissingUpstream_RespectsChainSubset(t *testing.T) {
	t.Parallel()

	handler, storState, _, _ := multiUpstreamTestSetup(t)
	// provider-1 satisfied; provider-2 has no token but is not in the chain.
	storState.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
		ProviderID:  "provider-1",
		AccessToken: "tok-1",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	got, err := handler.nextMissingUpstream(context.Background(), "test-session", []string{"provider-1"})
	require.NoError(t, err)
	assert.Empty(t, got, "provider-2 is not in the chain, so it must not be prompted")
}

func TestNextMissingUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupTokens func(st *testStorageState)
		want        string
		wantErr     bool
	}{
		{
			name: "all satisfied",
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
				st.upstreamTokens["test-session:provider-2"] = &storage.UpstreamTokens{
					ProviderID:  "provider-2",
					AccessToken: "tok-2",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
			},
			want: "",
		},
		{
			name:        "first missing",
			setupTokens: func(_ *testStorageState) {},
			want:        "provider-1",
		},
		{
			name: "provider-1 satisfied, provider-2 missing",
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
			},
			want: "provider-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, storState, _, _ := multiUpstreamTestSetup(t)
			tt.setupTokens(storState)

			got, err := handler.nextMissingUpstream(context.Background(), "test-session", twoUpstreamChain)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextMissingUpstream_RefreshExpired(t *testing.T) {
	t.Parallel()

	// provider-1 has an expired token (with a refresh token); provider-2 is live.
	// Presence alone would mark provider-1 satisfied; the refresh-then-classify
	// path must decide based on whether the expired leg can be refreshed.
	expiredProvider1 := func(st *testStorageState) {
		st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
			ProviderID:   "provider-1",
			AccessToken:  "tok-1",
			RefreshToken: "rt-1",
			ExpiresAt:    time.Now().Add(-time.Minute),
		}
		st.upstreamTokens["test-session:provider-2"] = &storage.UpstreamTokens{
			ProviderID:  "provider-2",
			AccessToken: "tok-2",
			ExpiresAt:   time.Now().Add(time.Hour),
		}
	}

	tests := []struct {
		name string
		// setupRefresher configures the mock refresher's expectations. Nil means no
		// refresher is wired into the handler at all.
		setupRefresher func(*mocks.MockUpstreamTokenRefresher)
		setupTokens    func(*testStorageState)
		want           string
	}{
		{
			name: "expired leg refreshed successfully is satisfied",
			setupRefresher: func(r *mocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "test-session", gomock.Any()).
					Return(&storage.UpstreamTokens{ProviderID: "provider-1", AccessToken: "tok-1-new"}, nil).
					Times(1)
			},
			setupTokens: expiredProvider1,
			want:        "",
		},
		{
			name: "expired leg whose refresh fails is reported missing",
			setupRefresher: func(r *mocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "test-session", gomock.Any()).
					Return(nil, errors.New("refresh token expired")).
					Times(1)
			},
			setupTokens: expiredProvider1,
			want:        "provider-1",
		},
		{
			name: "expired leg with no refresh token is reported missing without calling refresher",
			setupRefresher: func(_ *mocks.MockUpstreamTokenRefresher) {
				// RefreshAndStore must not be called when there is no refresh token.
			},
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(-time.Minute),
				}
			},
			want: "provider-1",
		},
		{
			name:           "expired leg with no refresher configured is reported missing",
			setupRefresher: nil,
			setupTokens:    expiredProvider1,
			want:           "provider-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []Option
			if tt.setupRefresher != nil {
				ctrl := gomock.NewController(t)
				t.Cleanup(ctrl.Finish)
				refresher := mocks.NewMockUpstreamTokenRefresher(ctrl)
				tt.setupRefresher(refresher)
				opts = append(opts, WithUpstreamRefresher(refresher))
			}

			handler, storState, _, _ := multiUpstreamTestSetup(t, opts...)
			tt.setupTokens(storState)

			got, err := handler.nextMissingUpstream(context.Background(), "test-session", twoUpstreamChain)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextMissingUpstream_StorageError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	cfg := &server.AuthorizationServerParams{
		Issuer:               testAuthIssuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		SigningKeyID:         "test-key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
		AllowedAudiences:     []string{"https://api.example.com"},
	}

	oauth2Config, err := server.NewAuthorizationServerConfig(cfg)
	require.NoError(t, err)

	stor := mocks.NewMockStorage(ctrl)

	storageErr := errors.New("connection refused")
	stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, storageErr).Times(1)

	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (any, error) {
			return rsaKey, nil
		},
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	provider := compose.Compose(
		oauth2Config.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)

	mockUpstream1 := &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}
	mockUpstream2 := &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}

	handler, err := NewHandler(provider, oauth2Config, stor,
		[]NamedUpstream{
			{Name: "provider-1", Provider: mockUpstream1},
			{Name: "provider-2", Provider: mockUpstream2},
		},
	)
	require.NoError(t, err)

	got, err := handler.nextMissingUpstream(context.Background(), "test-session", twoUpstreamChain)
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to check upstream token state")
	assert.ErrorIs(t, err, storageErr)
	assert.Empty(t, got)
}

func TestHandler_issuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *server.AuthorizationServerConfig
		expected string
	}{
		{
			name: "returns configured AccessTokenIssuer",
			config: &server.AuthorizationServerConfig{
				Config: &fosite.Config{AccessTokenIssuer: "https://as.example.com"},
			},
			expected: "https://as.example.com",
		},
		{
			name: "returns empty string when AccessTokenIssuer is unset",
			config: &server.AuthorizationServerConfig{
				Config: &fosite.Config{},
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{config: tc.config}
			assert.Equal(t, tc.expected, h.issuer())
		})
	}
}
