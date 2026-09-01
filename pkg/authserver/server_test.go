// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
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
	cfg.UpstreamFactory = mockFactory
	ctx := context.Background()
	srv, err := newServer(ctx, cfg, stor)

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

// capturingSlogHandler records log records for assertions. slog's default
// handler is process-global, so tests using it must not run in parallel with
// other slog-capturing tests.
type capturingSlogHandler struct {
	sink *capturingSlogSink
	// attrs carries the slog.With(...) attributes in effect for this handler,
	// flattened into every rendered record by recordsContaining. Without this,
	// a secret leaked via slog.With("client_secret", s).Info(...) — the most
	// likely way one escapes during a refactor — would be invisible to the
	// leak-detection tests.
	attrs []slog.Attr
}

// capturingSlogSink is the shared record store behind a capturingSlogHandler
// and every handler derived from it via WithAttrs.
type capturingSlogSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func newCapturingSlogHandler() *capturingSlogHandler {
	return &capturingSlogHandler{sink: &capturingSlogSink{}}
}

func (*capturingSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	// Flatten the With(...) attributes into the record so recordsContaining
	// sees them alongside per-call attrs.
	r.AddAttrs(h.attrs...)
	h.sink.records = append(h.sink.records, r)
	return nil
}

func (h *capturingSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &capturingSlogHandler{sink: h.sink, attrs: append(h.attrs, attrs...)}
}
func (h *capturingSlogHandler) WithGroup(_ string) slog.Handler { return h }

func (h *capturingSlogHandler) messages(level slog.Level, containing string) []string {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	var out []string
	for _, r := range h.sink.records {
		if r.Level == level && strings.Contains(r.Message, containing) {
			out = append(out, r.Message)
		}
	}
	return out
}

// recordsContaining returns every captured record — message plus all
// attribute values, at any level — that contains needle. Used by leak-detection
// tests that must prove a secret appears in ZERO log records, not just in
// zero messages.
func (h *capturingSlogHandler) recordsContaining(needle string) []string {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	var out []string
	for _, r := range h.sink.records {
		var b strings.Builder
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" ")
			b.WriteString(a.Value.String())
			return true
		})
		if strings.Contains(b.String(), needle) {
			out = append(out, b.String())
		}
	}
	return out
}

// TestNewServer_AllowConfidentialClientRegistration_Logs pins the startup logging
// contract: enabling the flag logs an Info naming the consequence when
// startup succeeds. Combining it with insecure_allow_http is rejected by
// Config.Validate (see ValidateConfidentialClientTransport) before this log
// line is ever reached, so that combination is covered by
// TestConfig_Validate_RejectsConfidentialClientOverInsecureHTTP instead.
//
//nolint:paralleltest // swaps the process-global slog default handler
func TestNewServer_AllowConfidentialClientRegistration_Logs(t *testing.T) {
	// Not parallel: swaps the process-global slog default handler.

	newCfg := func(allowConfidential bool) Config {
		return Config{
			Issuer:                              "https://example.com",
			KeyProvider:                         keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets:                         &servercrypto.HMACSecrets{Current: validHMACSecret()},
			Upstreams:                           []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
			AllowedAudiences:                    []string{"https://mcp.example.com"},
			AllowConfidentialClientRegistration: allowConfidential,
		}
	}

	tests := []struct {
		name              string
		allowConfidential bool
		wantInfo          bool
	}{
		{"flag off: no logs", false, false},
		{"flag on: Info naming the consequence", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := newCapturingSlogHandler()
			prev := slog.Default()
			slog.SetDefault(slog.New(capture))
			t.Cleanup(func() { slog.SetDefault(prev) })

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockUpstream := upstreammocks.NewMockOAuth2Provider(ctrl)
			mockFactory := func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
				return mockUpstream, nil
			}
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })

			cfg := newCfg(tt.allowConfidential)
			cfg.UpstreamFactory = mockFactory
			srv, err := newServer(context.Background(), cfg, stor)
			require.NoError(t, err, "startup must succeed for every flag combination")
			require.NotNil(t, srv)

			// Filter to the flag's own log lines: other components (key
			// generation, baseline scopes) also log at Info during startup.
			infos := capture.messages(slog.LevelInfo, "client secrets")

			if tt.wantInfo {
				require.Len(t, infos, 1)
				assert.Contains(t, infos[0], "unauthenticated dynamic registration")
			} else {
				assert.Empty(t, infos)
			}
		})
	}
}

// TestConfig_Validate_RejectsConfidentialClientOverInsecureHTTP pins the
// rejection of allow_confidential_client_registration combined with insecure_allow_http:
// issuing client secrets over cleartext HTTP on an unauthenticated
// registration endpoint must fail loudly at config validation, not just log
// a warning.
func TestConfig_Validate_RejectsConfidentialClientOverInsecureHTTP(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Issuer:                              "http://example.com",
		KeyProvider:                         keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:                         &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:                           []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences:                    []string{"https://mcp.example.com"},
		AllowConfidentialClientRegistration: true,
		InsecureAllowHTTP:                   true,
	}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow_confidential_client_registration")
	assert.Contains(t, err.Error(), "insecure_allow_http")
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

	cfg.UpstreamFactory = mockFactory
	srv, err := newServer(context.Background(), cfg, stor)
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

	cfg.UpstreamFactory = mockFactory
	srv, err := newServer(context.Background(), cfg, stor)
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

func TestNewServer_RegistersDelegateClientsBeforeUpstreamConstruction(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	cfg := Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
		AllowedAudiences: []string{"https://mcp.example.com"},
		DelegateClients: []DelegateClient{{
			ClientID:     "delegate",
			ClientSecret: "delegate-secret-well-above-the-minimum-length",
			Scopes:       []string{"openid"}, Audiences: []string{"https://mcp.example.com"},
		}},
	}

	factory := func(ctx context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
		client, err := stor.GetClient(ctx, "delegate")
		require.NoError(t, err)
		assert.False(t, registration.DCRIssued(client))
		assert.False(t, client.IsPublic())
		return nil, assert.AnError
	}

	cfg.UpstreamFactory = factory
	_, err := newServer(ctx, cfg, stor)
	require.ErrorIs(t, err, assert.AnError)

	// Startup registration is an upsert. Replacing a same-ID DCR client makes
	// it permanent and unmarked rather than retaining DCR eviction semantics.
	dcrClient, err := registration.NewConfidentialPlain(registration.Config{ID: "delegate", Secret: "old-secret"})
	require.NoError(t, err)
	require.NoError(t, stor.RegisterClient(ctx, dcrClient))
	_, err = newServer(ctx, cfg, stor)
	require.ErrorIs(t, err, assert.AnError)
	client, err := stor.GetClient(ctx, "delegate")
	require.NoError(t, err)
	assert.False(t, registration.DCRIssued(client))
}

// closeCountingProvider is an OAuth2Provider that implements the optional
// upstream.IdleConnectionCloser capability and records invocations.
type closeCountingProvider struct {
	upstream.OAuth2Provider
	closes  atomic.Int32
	onClose func()
}

func (p *closeCountingProvider) CloseIdleConnections() {
	p.closes.Add(1)
	if p.onClose != nil {
		p.onClose()
	}
}

// plainProvider is an OAuth2Provider that does NOT implement
// upstream.IdleConnectionCloser, standing in for an external implementation of
// the open interface.
type plainProvider struct {
	upstream.OAuth2Provider
}

// eventRecordingStorage records when the server closes storage, so the ordering
// between upstream draining and storage teardown can be asserted. Close does not
// delegate: MemoryStorage.Close panics when called twice and the test cleanup
// closes the underlying store.
type eventRecordingStorage struct {
	*storage.MemoryStorage
	record func(string)
}

func (s *eventRecordingStorage) Unwrap() storage.Storage { return s.MemoryStorage }

func (s *eventRecordingStorage) Close() error {
	s.record("storage")
	return nil
}

// TestServer_CloseIdleConnections pins the Close/CloseIdleConnections split that
// lets an embedder retire a superseded server without closing the storage a
// replacement server is still serving through (see #6479). It also pins that
// providers lacking the optional capability are skipped rather than panicking.
func TestServer_CloseIdleConnections(t *testing.T) {
	t.Parallel()

	newTestServer := func(t *testing.T, record func(string), providers ...upstream.OAuth2Provider) *server {
		t.Helper()

		base := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = base.Close() })
		stor := &eventRecordingStorage{MemoryStorage: base, record: record}

		upstreams := make([]UpstreamConfig, 0, len(providers))
		for i := range providers {
			upstreams = append(upstreams, UpstreamConfig{
				Name:         fmt.Sprintf("upstream-%d", i),
				Type:         UpstreamProviderTypeOAuth2,
				OAuth2Config: validUpstreamConfig(),
			})
		}

		next := 0
		factory := func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
			p := providers[next]
			next++
			return p, nil
		}

		srv, err := newServer(t.Context(), Config{
			Issuer:           "https://example.com",
			KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
			Upstreams:        upstreams,
			AllowedAudiences: []string{"https://mcp.example.com"},
			UpstreamFactory:  factory,
		}, stor)
		require.NoError(t, err)
		return srv
	}

	t.Run("drains every capable upstream without closing storage", func(t *testing.T) {
		t.Parallel()

		var events []string
		first, second := &closeCountingProvider{}, &closeCountingProvider{}
		srv := newTestServer(t, func(e string) { events = append(events, e) }, first, second)

		srv.CloseIdleConnections()

		assert.Equal(t, int32(1), first.closes.Load())
		assert.Equal(t, int32(1), second.closes.Load())
		// Leaving storage open is the whole point of the split: an embedder
		// retiring a superseded server must not tear down the storage the
		// replacement server is serving through.
		assert.Empty(t, events, "CloseIdleConnections must not touch storage")
	})

	t.Run("Close drains upstreams then closes storage", func(t *testing.T) {
		t.Parallel()

		var events []string
		record := func(e string) { events = append(events, e) }
		provider := &closeCountingProvider{}
		provider.onClose = func() { record("upstream") }
		srv := newTestServer(t, record, provider)

		require.NoError(t, srv.Close())

		assert.Equal(t, int32(1), provider.closes.Load())
		assert.Equal(t, []string{"upstream", "storage"}, events)
	})

	t.Run("skips upstreams without the capability", func(t *testing.T) {
		t.Parallel()

		capable := &closeCountingProvider{}
		srv := newTestServer(t, func(string) {}, &plainProvider{}, capable)

		assert.NotPanics(t, srv.CloseIdleConnections)
		assert.Equal(t, int32(1), capable.closes.Load())
	})
}

// TestNewServer_UpstreamFactory pins that a caller can own upstream
// construction through Config.UpstreamFactory, and that DefaultUpstreamFactory
// is used when the field is nil.
func TestNewServer_UpstreamFactory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setFactory bool
		// wantCustom is true when the caller's factory should have run; false
		// means DefaultUpstreamFactory built a real BaseOAuth2Provider from
		// validUpstreamConfig.
		wantCustom bool
	}{
		{name: "config factory is used", setFactory: true, wantCustom: true},
		{name: "default factory is used when the field is nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })

			var called bool
			cfg := Config{
				Issuer:           "https://example.com",
				KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
				HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
				Upstreams:        []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()}},
				AllowedAudiences: []string{"https://mcp.example.com"},
			}
			if tt.setFactory {
				cfg.UpstreamFactory = func(_ context.Context, _ *UpstreamConfig) (upstream.OAuth2Provider, error) {
					called = true
					return &plainProvider{}, nil
				}
			}

			srv, err := newServer(t.Context(), cfg, stor)
			require.NoError(t, err)
			// The default factory builds a provider with a live pool; drain it
			// so the suite does not leak the resource this change fixes.
			t.Cleanup(srv.CloseIdleConnections)

			require.Len(t, srv.upstreams, 1)
			assert.Equal(t, tt.wantCustom, called)
			if !tt.wantCustom {
				assert.IsType(t, &upstream.BaseOAuth2Provider{}, srv.upstreams[0].Provider)
			}
		})
	}
}

// TestBuildUpstreams_DrainsAlreadyBuiltProvidersOnFailure pins the pathological
// case from #6479: a caller retrying a reconstruction against an unreachable
// issuer must not accumulate a live connection pool per attempt for the
// upstreams that did construct before the failure.
func TestBuildUpstreams_DrainsAlreadyBuiltProvidersOnFailure(t *testing.T) {
	t.Parallel()

	built := &closeCountingProvider{}
	cfg := Config{
		Upstreams: []UpstreamConfig{
			{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
			{Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
		},
		UpstreamFactory: func(_ context.Context, upCfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
			if upCfg.Name == "second" {
				return nil, assert.AnError
			}
			return built, nil
		},
	}

	upstreams, err := buildUpstreams(t.Context(), cfg)
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), `"second"`, "the error must name the failing upstream")
	assert.Nil(t, upstreams)
	assert.Equal(t, int32(1), built.closes.Load(),
		"the provider built before the failure must be drained")
}

// TestBuildUpstreams_RejectsNilProvider pins that a custom
// Config.UpstreamFactory cannot drop an upstream by returning (nil, nil). Such a
// NamedUpstream would be wired into the authorization chain and panic on the
// first /oauth/authorize request, after any startup health check had passed.
func TestBuildUpstreams_RejectsNilProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider upstream.OAuth2Provider
	}{
		// A nil interface value.
		{name: "nil interface"},
		// An interface holding a nil pointer — non-nil as an interface, so it
		// passes a bare `== nil` check, satisfies the IdleConnectionCloser
		// assertion, and then nil-derefs on the embedded *BaseOAuth2Provider
		// during the drain. An easy mistake in the "substitute a provider"
		// pattern Config.UpstreamFactory documents.
		{name: "typed nil pointer", provider: (*upstream.OIDCProviderImpl)(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			built := &closeCountingProvider{}
			cfg := Config{
				Upstreams: []UpstreamConfig{
					{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
					{Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstreamConfig()},
				},
				UpstreamFactory: func(_ context.Context, upCfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
					if upCfg.Name == "second" {
						return tt.provider, nil
					}
					return built, nil
				},
			}

			upstreams, err := buildUpstreams(t.Context(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `nil provider for upstream "second"`)
			assert.Nil(t, upstreams)
			assert.Equal(t, int32(1), built.closes.Load(),
				"the provider built before the rejection must be drained")
		})
	}
}

// TestServer_Close_DrainsRealOIDCUpstream exercises the production wiring that
// the other tests stub out: a server built through DefaultUpstreamFactory holds
// an *upstream.OIDCProviderImpl, which satisfies upstream.IdleConnectionCloser
// only by promotion from its embedded *BaseOAuth2Provider. The pool is observed
// from the issuer's side, so this fails if the type assertion in
// closeUpstreamIdleConnections ever silently stops matching.
func TestServer_Close_DrainsRealOIDCUpstream(t *testing.T) {
	t.Parallel()

	var openConns atomic.Int32
	issuer := httptest.NewUnstartedServer(nil)
	issuer.Config.Handler = oidcDiscoveryHandler(func() string { return issuer.URL })
	issuer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			openConns.Add(1)
		case http.StateClosed, http.StateHijacked:
			openConns.Add(-1)
		case http.StateActive, http.StateIdle:
		}
	}
	issuer.Start()
	t.Cleanup(issuer.Close)

	stor := storage.NewMemoryStorage()
	var closeOnce sync.Once
	closeStorage := func() { closeOnce.Do(func() { _ = stor.Close() }) }
	// Registered so an aborted require below does not leave storage running;
	// idempotent because srv.Close() closes the same store, and
	// MemoryStorage.Close panics if called twice.
	t.Cleanup(closeStorage)

	srv, err := newServer(t.Context(), Config{
		Issuer:      "https://example.com",
		KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets: &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams: []UpstreamConfig{{
			Name: "oidc",
			Type: UpstreamProviderTypeOIDC,
			OIDCConfig: &upstream.OIDCConfig{
				CommonOAuthConfig: upstream.CommonOAuthConfig{
					ClientID:    "test-client",
					RedirectURI: "https://example.com/callback",
					Scopes:      []string{"openid"},
				},
				Issuer:            issuer.URL,
				InsecureAllowHTTP: true,
				AllowPrivateIPs:   true,
			},
		}},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}, stor)
	require.NoError(t, err)
	require.IsType(t, &upstream.OIDCProviderImpl{}, srv.upstreams[0].Provider)

	// Discovery leaves at least one idle keep-alive connection behind; before
	// this change it was retained for the lifetime of the process. Not an exact
	// count — go-oidc is free to add a fetch or a retry, and ConnState fires on
	// the httptest server's own goroutines.
	require.GreaterOrEqual(t, openConns.Load(), int32(1), "discovery must leave a pooled connection")

	require.NoError(t, srv.Close())
	closeOnce.Do(func() {}) // srv.Close closed the store; disarm the cleanup

	// The issuer observes the close asynchronously.
	assert.Eventually(t, func() bool { return openConns.Load() == 0 }, 5*time.Second, 10*time.Millisecond,
		"Close must drain the OIDC upstream's pooled connection")
}

// oidcDiscoveryHandler serves the minimum OIDC discovery document
// upstream.NewOIDCProvider accepts. issuerURL is a func because httptest only
// knows the bound URL after Start.
func oidcDiscoveryHandler(issuerURL func() string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		base := issuerURL()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/auth",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	return mux
}
