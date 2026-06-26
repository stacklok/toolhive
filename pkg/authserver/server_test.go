// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// validUpstreamConfig returns a valid upstream config for tests.
func validUpstreamConfig() *upstream.OAuth2Config {
	return &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:    "test-client",
			RedirectURI: "https://example.com/callback",
		},
		AuthorizationEndpoint: "https://idp.example.com/auth",
		TokenEndpoint:         "https://idp.example.com/token",
	}
}

// validHMACSecret returns a valid HMAC secret for tests.
func validHMACSecret() []byte {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return secret
}

func TestNew(t *testing.T) {
	t.Parallel()

	validKeyProvider := keys.NewGeneratingProvider(keys.DefaultAlgorithm)
	validHMAC := &servercrypto.HMACSecrets{Current: validHMACSecret()}
	validUpstreams := []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}}

	tests := []struct {
		name        string
		cfg         Config
		storageNil  bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil storage returns error",
			cfg:         Config{},
			storageNil:  true,
			wantErr:     true,
			errContains: "invalid config",
		},
		{
			name:        "empty issuer returns error",
			cfg:         Config{},
			storageNil:  false,
			wantErr:     true,
			errContains: "issuer is required",
		},
		// Note: "missing HMAC secrets" no longer returns an error because
		// applyDefaults() auto-generates them when nil
		{
			name: "HMAC secret too short returns error",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      validKeyProvider,
				HMACSecrets:      &servercrypto.HMACSecrets{Current: []byte("short")},
				Upstreams:        validUpstreams,
				AllowedAudiences: []string{"https://mcp.example.com"},
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "HMAC secret must be at least 32 bytes",
		},
		{
			name: "missing upstreams returns error",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      validKeyProvider,
				HMACSecrets:      validHMAC,
				AllowedAudiences: []string{"https://mcp.example.com"},
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "at least one upstream is required",
		},
		{
			name: "missing allowed audiences returns error",
			cfg: Config{
				Issuer:      "https://example.com",
				KeyProvider: validKeyProvider,
				HMACSecrets: validHMAC,
				Upstreams:   validUpstreams,
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "at least one allowed audience is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			var stor *storagemocks.MockStorage
			if !tt.storageNil {
				stor = storagemocks.NewMockStorage(ctrl)
			}

			ctx := context.Background()
			_, err := New(ctx, tt.cfg, stor)

			if tt.wantErr {
				if err == nil {
					t.Errorf("New() error = nil, wantErr %v", tt.wantErr)
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("New() error = %q, want error containing %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("New() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestNewServer_Success tests the success path with mocked dependencies.
func TestNewServer_Success(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockUpstream := upstreammocks.NewMockOAuth2Provider(ctrl)

	// Use a real MemoryStorage rather than storagemocks.MockStorage: the
	// constructor type-asserts the storage to storage.DCRCredentialStore (per
	// the F6 design — Storage no longer embeds DCRCredentialStore), and
	// generated MockStorage does not implement DCRCredentialStore. This test
	// exercises the constructor flow, not specific storage method calls, so
	// a real MemoryStorage is sufficient and keeps the assertion path real.
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })

	// Create valid config
	cfg := Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	// Create factory that returns our mock
	mockFactory := func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
		return mockUpstream, nil
	}

	// Call newServer with the mock factory
	ctx := context.Background()
	srv, err := newServer(ctx, cfg, stor, withUpstreamFactory(mockFactory))

	if err != nil {
		t.Fatalf("newServer() unexpected error: %v", err)
	}
	if srv == nil {
		t.Fatal("newServer() returned nil server")
	}
	if srv.Handler() == nil {
		t.Error("server.Handler() returned nil")
	}
	if srv.IDPTokenStorage() != stor {
		t.Error("server.IDPTokenStorage() did not return expected storage")
	}
}

func TestNewServer_CIMDEnabled_WrapsStorage(t *testing.T) {
	t.Parallel()

	mockUpstream := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })

	cfg := Config{
		Issuer:               "https://example.com",
		KeyProvider:          keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:          &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:            []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences:     []string{"https://mcp.example.com"},
		CIMDEnabled:          true,
		CIMDCacheMaxSize:     16,
		CIMDCacheFallbackTTL: 5 * time.Minute,
	}

	mockFactory := func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
		return mockUpstream, nil
	}

	srv, err := newServer(context.Background(), cfg, stor, withUpstreamFactory(mockFactory))
	if err != nil {
		t.Fatalf("newServer() unexpected error: %v", err)
	}

	_, ok := srv.storage.(*storage.CIMDStorageDecorator)
	if !ok {
		t.Errorf("expected storage to be *storage.CIMDStorageDecorator when CIMDEnabled=true, got %T", srv.storage)
	}
}

// TestNewServer_UpstreamRefresherSharedInstance verifies the wiring this PR
// fixes: UpstreamTokenRefresher() must return the single refresher constructed
// in newServer rather than reallocating one per call. The pre-fix accessor
// rebuilt the refresher (and its singleflight.Group) on every call, so the
// handler chain-walk path and the runtime token-swap path ended up with
// independent groups and cross-path refresh deduplication was impossible.
// A regression that reintroduced per-call allocation would leave the
// refresher's own singleflight test green, so this asserts instance identity
// at the server boundary instead.
func TestNewServer_UpstreamRefresherSharedInstance(t *testing.T) {
	t.Parallel()

	mockUpstream := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })

	cfg := Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}
	mockFactory := func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
		return mockUpstream, nil
	}

	srv, err := newServer(context.Background(), cfg, stor, withUpstreamFactory(mockFactory))
	require.NoError(t, err)

	first := srv.UpstreamTokenRefresher()
	require.NotNil(t, first, "refresher must be non-nil when upstreams are configured")
	// Repeated calls must return the identical instance — i.e. the same
	// singleflight.Group — not a freshly allocated one.
	assert.Same(t, first, srv.UpstreamTokenRefresher(),
		"UpstreamTokenRefresher() must return the shared instance, not reallocate per call")
	// That instance must be the field stored on the server, which is the same
	// value wired into the handler via WithUpstreamRefresher in newServer.
	assert.Same(t, srv.upstreamRefresher, first,
		"accessor must return the stored instance shared with the handler")
}

// TestNewUpstreamTokenRefresher_NilWhenNoUpstreams verifies the true-nil
// interface contract: with no upstreams the constructor must return a nil
// interface value, not a typed nil (*upstreamTokenRefresher)(nil) wrapped in an
// interface, so that callers' `== nil` checks (runner, service, handler) work.
func TestNewUpstreamTokenRefresher_NilWhenNoUpstreams(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })

	refresher := newUpstreamTokenRefresher(nil, stor, 24*time.Hour)
	// Direct == nil comparison, not assert.Nil: testify's Nil also passes for a
	// typed nil pointer, which would hide exactly the bug this guards against.
	if refresher != nil {
		t.Fatalf("expected a true nil interface, got non-nil %T", refresher)
	}
}
