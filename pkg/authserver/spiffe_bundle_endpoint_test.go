// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/federation"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/networking"
)

type testSPIFFEBundleSource struct {
	mu     sync.RWMutex
	bundle *spiffebundle.Bundle
}

func (s *testSPIFFEBundleSource) GetBundleForTrustDomain(_ spiffeid.TrustDomain) (*spiffebundle.Bundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bundle, nil
}

func (s *testSPIFFEBundleSource) set(bundle *spiffebundle.Bundle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bundle = bundle
}

type testSPIFFEBundleEndpointTransport struct {
	response *http.Response
}

func (t *testSPIFFEBundleEndpointTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return t.response, nil
}

type testEndlessResponseBody struct {
	bytesRead atomic.Int64
	closed    atomic.Bool
}

func (b *testEndlessResponseBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.bytesRead.Add(int64(len(p)))
	return len(p), nil
}

func (b *testEndlessResponseBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestSPIFFEBundleEndpointSourceFetchesFederationBundle(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	served := &testSPIFFEBundleSource{bundle: testSPIFFEBundle(trustDomain, 1)}
	var query url.Values
	handler, err := federation.NewHandler(trustDomain, served)
	require.NoError(t, err)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.Query()
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)
	require.NoError(t, source.refresh(context.Background()))

	bundle, err := source.GetBundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	sequence, ok := bundle.SequenceNumber()
	require.True(t, ok)
	require.Equal(t, uint64(1), sequence)
	require.Equal(t, "spiffe://example.org", query.Get("spiffe_id"))
	lastSuccess := time.Now().Add(-time.Second)
	source.lifecycleMu.Lock()
	source.lastSuccess = lastSuccess
	source.lifecycleMu.Unlock()
	require.NoError(t, source.refresh(context.Background()))
	source.lifecycleMu.RLock()
	refreshedAt := source.lastSuccess
	source.lifecycleMu.RUnlock()
	require.True(t, refreshedAt.After(lastSuccess))
}

func TestSPIFFEBundleEndpointSourceRejectsInvalidResponsesWithoutReplacingLastKnownGood(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{
			name: "non-success status",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusServiceUnavailable)
			}),
		},
		{
			name: "oversized body",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(make([]byte, spiffeBundleEndpointMaxBodyBytes+1))
			}),
		},
		{
			name: "malformed body",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("not a bundle"))
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewTLSServer(tt.handler)
			t.Cleanup(server.Close)
			source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)
			current := testSPIFFEBundle(trustDomain, 1)
			source.bundle.Store(current)
			source.lifecycleMu.Lock()
			source.lastSuccess = time.Now()
			source.lifecycleMu.Unlock()

			require.Error(t, source.refresh(context.Background()))
			actual, err := source.GetBundleForTrustDomain(trustDomain)
			require.NoError(t, err)
			require.Same(t, current, actual)
		})
	}
}

func TestSPIFFEBundleEndpointSourceRejectsMissingEnabledAuthoritiesWithoutReplacingLastKnownGood(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	tests := []struct {
		name             string
		methods          []SPIFFEAuthenticationMethod
		initial, fetched *spiffebundle.Bundle
		wantErr          string
	}{
		{
			name:    "X.509 only",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
			initial: testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "x509", ""),
			fetched: testSPIFFEBundleWithAuthorities(t, trustDomain, 2, "", "jwt"),
			wantErr: "no X.509 authorities",
		},
		{
			name:    "JWT only",
			methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
			initial: testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "", "jwt"),
			fetched: testSPIFFEBundleWithAuthorities(t, trustDomain, 2, "x509", ""),
			wantErr: "no JWT authorities",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			served := &testSPIFFEBundleSource{bundle: tt.initial}
			handler, err := federation.NewHandler(trustDomain, served)
			require.NoError(t, err)
			server := httptest.NewTLSServer(handler)
			t.Cleanup(server.Close)
			source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server, tt.methods...)

			require.NoError(t, source.refresh(context.Background()))
			current := source.bundle.Load()
			served.set(tt.fetched)

			require.ErrorContains(t, source.refresh(context.Background()), tt.wantErr)
			require.Same(t, current, source.bundle.Load())
		})
	}
}

func TestSPIFFEBundleEndpointSourceBoundsResponseDrain(t *testing.T) {
	t.Parallel()

	body := &testEndlessResponseBody{}
	source := &spiffeBundleEndpointSource{
		trustDomain: spiffeid.RequireTrustDomainFromString("example.org"),
		endpoint:    url.URL{Scheme: "https", Host: "example.org"},
		client: &http.Client{Transport: &testSPIFFEBundleEndpointTransport{
			response: &http.Response{StatusCode: http.StatusServiceUnavailable, Body: body},
		}},
	}

	require.Error(t, source.refresh(context.Background()))
	require.Equal(t, int64(spiffeBundleEndpointMaxBodyBytes), body.bytesRead.Load())
	require.True(t, body.closed.Load())
}

func TestSPIFFEBundleEndpointSourceSequencePolicy(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	tests := []struct {
		name         string
		current      *spiffebundle.Bundle
		next         *spiffebundle.Bundle
		wantError    bool
		wantReplaced bool
	}{
		{name: "unsequenced to unsequenced", current: testSPIFFEBundle(trustDomain), next: testSPIFFEBundle(trustDomain), wantReplaced: true},
		{name: "unsequenced to sequenced", current: testSPIFFEBundle(trustDomain), next: testSPIFFEBundle(trustDomain, 1), wantReplaced: true},
		{name: "sequenced to unsequenced", current: testSPIFFEBundle(trustDomain, 1), next: testSPIFFEBundle(trustDomain), wantError: true},
		{name: "lower sequence", current: testSPIFFEBundle(trustDomain, 2), next: testSPIFFEBundle(trustDomain, 1), wantError: true},
		{name: "equal sequence", current: testSPIFFEBundle(trustDomain, 1), next: testSPIFFEBundle(trustDomain, 1)},
		{name: "higher sequence", current: testSPIFFEBundle(trustDomain, 1), next: testSPIFFEBundle(trustDomain, 2), wantReplaced: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &spiffeBundleEndpointSource{trustDomain: trustDomain}
			source.bundle.Store(tt.current)

			installed, err := source.storeBundle(tt.next)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantReplaced, installed)
			if tt.wantReplaced {
				require.Same(t, tt.next, source.bundle.Load())
			} else {
				require.Same(t, tt.current, source.bundle.Load())
			}
		})
	}
}

func TestSPIFFEBundleEndpointSourceRotatesWholeBundle(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	served := &testSPIFFEBundleSource{bundle: testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "old", "old")}
	handler, err := federation.NewHandler(trustDomain, served)
	require.NoError(t, err)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)

	require.NoError(t, source.refresh(context.Background()))
	first := source.bundle.Load()
	served.set(testSPIFFEBundleWithAuthorities(t, trustDomain, 2, "new", "new"))
	require.NoError(t, source.refresh(context.Background()))
	second := source.bundle.Load()

	require.NotSame(t, first, second)
	sequence, ok := second.SequenceNumber()
	require.True(t, ok)
	require.Equal(t, uint64(2), sequence)
	require.Len(t, second.X509Authorities(), 1)
	require.Equal(t, "new", second.X509Authorities()[0].Subject.CommonName)
	require.NotContains(t, second.JWTAuthorities(), "jwt-old")
	require.Contains(t, second.JWTAuthorities(), "jwt-new")

	served.set(testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "old", "old"))
	require.ErrorContains(t, source.refresh(context.Background()), "sequence number regressed")
	require.Same(t, second, source.bundle.Load())
}

func TestSPIFFEBundleEndpointSourceLastKnownGoodAndContextLifecycle(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	served := &testSPIFFEBundleSource{bundle: testSPIFFEBundle(trustDomain, 1)}
	federationHandler, err := federation.NewHandler(trustDomain, served)
	require.NoError(t, err)
	var failRequests atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if failRequests.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		federationHandler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)
	t.Cleanup(source.close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, source.start(ctx))
	cancel()
	select {
	case <-source.done:
		t.Fatal("startup context stopped the SPIFFE bundle refresh loop")
	case <-time.After(50 * time.Millisecond):
	}

	failRequests.Store(true)
	require.Error(t, source.refresh(context.Background()))
	_, err = source.GetBundleForTrustDomain(trustDomain)
	require.NoError(t, err)

	source.lifecycleMu.Lock()
	source.lastSuccess = time.Now().Add(-48 * time.Hour)
	source.lifecycleMu.Unlock()
	_, err = source.GetBundleForTrustDomain(trustDomain)
	require.NoError(t, err)

	source.close()
	select {
	case <-source.done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SPIFFE bundle refresh loop to stop")
	}
	require.ErrorIs(t, source.start(context.Background()), errSPIFFEBundleSourceClosed)
}

func TestSPIFFEBundleEndpointSourceCloseBeforeStartIsTerminal(t *testing.T) {
	t.Parallel()

	source, err := newSPIFFEBundleEndpointSource(
		spiffeid.RequireTrustDomainFromString("example.org"),
		[]SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT},
		"https://example.org/bundle",
	)
	require.NoError(t, err)

	source.close()
	source.close()
	require.ErrorIs(t, source.start(context.Background()), errSPIFFEBundleSourceClosed)
}

func TestSPIFFEBundleEndpointSourceFailedInitialStartIsRetryable(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	served := &testSPIFFEBundleSource{bundle: testSPIFFEBundle(trustDomain, 1)}
	federationHandler, err := federation.NewHandler(trustDomain, served)
	require.NoError(t, err)
	var available atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !available.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		federationHandler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)
	t.Cleanup(source.close)

	require.Error(t, source.start(context.Background()))
	available.Store(true)
	require.NoError(t, source.start(context.Background()))
}

func TestSPIFFEBundleEndpointSourceCloseCancelsInitialFetch(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	source := newTestSPIFFEBundleEndpointSource(t, spiffeid.RequireTrustDomainFromString("example.org"), server)
	t.Cleanup(source.close)

	startDone := make(chan error, 1)
	go func() {
		startDone <- source.start(context.Background())
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial bundle request")
	}

	closeDone := make(chan struct{})
	go func() {
		source.close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("timed out closing source during initial bundle fetch")
	}
	select {
	case err := <-startDone:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial start to return")
	}
	require.ErrorIs(t, source.start(context.Background()), errSPIFFEBundleSourceClosed)
}

func TestSPIFFEBundleEndpointSourceSecurityAndRefreshBounds(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	for _, endpoint := range []string{
		"http://example.org/bundle",
		"https:///bundle",
		"https://user:password@example.org/bundle",
		"https://example.org/bundle?query=value",
		"https://example.org/bundle#fragment",
		"https://localhost/bundle",
		"https://127.0.0.1/bundle",
	} {
		_, err := newSPIFFEBundleEndpointSource(trustDomain, []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, endpoint)
		require.Error(t, err)
	}

	source, err := newSPIFFEBundleEndpointSource(trustDomain, []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}, "https://example.org/bundle")
	require.NoError(t, err)
	validatingTransport, ok := source.client.Transport.(*networking.ValidatingTransport)
	require.True(t, ok)
	transport, ok := validatingTransport.Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, transport.DisableKeepAlives)
	require.NotNil(t, transport.DialContext)
	_, err = transport.DialContext(context.Background(), "tcp", "10.0.0.1:443")
	require.Error(t, err)
	require.Error(t, source.client.CheckRedirect(
		&http.Request{URL: &url.URL{Scheme: "https", Host: "other.example.org"}},
		[]*http.Request{{URL: &url.URL{Scheme: "https", Host: "example.org"}}},
	))

	source = &spiffeBundleEndpointSource{trustDomain: trustDomain}
	for _, hint := range []time.Duration{time.Second, 24 * time.Hour} {
		bundle := testSPIFFEBundle(trustDomain)
		bundle.SetRefreshHint(hint)
		source.bundle.Store(bundle)
		interval := source.refreshInterval()
		floor := min(max(hint, spiffeBundleEndpointMinRefresh), spiffeBundleEndpointMaxRefresh)
		require.GreaterOrEqual(t, interval, time.Duration(float64(floor)*0.9))
		require.LessOrEqual(t, interval, time.Duration(float64(floor)*1.1))
	}
}

func newTestSPIFFEBundleEndpointSource(
	t *testing.T,
	trustDomain spiffeid.TrustDomain,
	server *httptest.Server,
	methods ...SPIFFEAuthenticationMethod,
) *spiffeBundleEndpointSource {
	t.Helper()
	if len(methods) == 0 {
		methods = []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodJWT}
	}
	source, err := newSPIFFEBundleEndpointSource(trustDomain, methods, "https://bundles.example.org")
	require.NoError(t, err)
	// Tests use a local TLS server, while production configuration rejects loopback
	// bundle endpoints. Substitute both endpoint and trusted test transport here.
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	source.endpoint = *parsed
	source.client = server.Client()
	return source
}

func testSPIFFEBundle(trustDomain spiffeid.TrustDomain, sequence ...uint64) *spiffebundle.Bundle {
	bundle := spiffebundle.New(trustDomain)
	seed := make([]byte, ed25519.SeedSize)
	if len(sequence) > 0 {
		seed[len(seed)-1] = byte(sequence[0])
		bundle.SetSequenceNumber(sequence[0])
	}
	if err := bundle.AddJWTAuthority(fmt.Sprintf("key-%d", seed[len(seed)-1]), ed25519.NewKeyFromSeed(seed).Public()); err != nil {
		panic(err)
	}
	return bundle
}

func testSPIFFEBundleWithAuthorities(
	t *testing.T,
	trustDomain spiffeid.TrustDomain,
	sequence uint64,
	x509Name, jwtName string,
) *spiffebundle.Bundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)
	certificateDER, err := x509.CreateCertificate(
		cryptorand.Reader,
		&x509.Certificate{
			SerialNumber:          big.NewInt(int64(sequence)),
			Subject:               pkix.Name{CommonName: x509Name},
			NotBefore:             time.Now().Add(-time.Minute),
			NotAfter:              time.Now().Add(time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		},
		&x509.Certificate{
			SerialNumber:          big.NewInt(int64(sequence)),
			Subject:               pkix.Name{CommonName: x509Name},
			NotBefore:             time.Now().Add(-time.Minute),
			NotAfter:              time.Now().Add(time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		},
		key.Public(),
		key,
	)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(certificateDER)
	require.NoError(t, err)

	bundle := spiffebundle.New(trustDomain)
	bundle.SetSequenceNumber(sequence)
	if x509Name != "" {
		bundle.AddX509Authority(certificate)
	}
	if jwtName != "" {
		seed := make([]byte, ed25519.SeedSize)
		seed[len(seed)-1] = byte(sequence)
		require.NoError(t, bundle.AddJWTAuthority("jwt-"+jwtName, ed25519.NewKeyFromSeed(seed).Public()))
	}
	return bundle
}

func TestSPIFFEBundleEndpointSourceDoesNotLeakBundleMaterialInRefreshLogs(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	source := &spiffeBundleEndpointSource{
		trustDomain: trustDomain,
		endpoint: url.URL{
			Scheme: "https",
			Host:   "credentials-are-not-loggable.example.org",
			User:   url.UserPassword("sensitive-user", "sensitive-password"),
		},
	}
	source.bundle.Store(testSPIFFEBundleWithAuthorities(t, trustDomain, 7, "sensitive-certificate", "sensitive-jwt-key"))
	source.lifecycleMu.Lock()
	source.lastSuccess = time.Now().Add(-time.Minute)
	source.lifecycleMu.Unlock()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	source.logRefresh(logger, 5*time.Minute, nil)
	source.logRefresh(logger, 5*time.Second, errors.New("sensitive raw bundle body"))

	output := logs.String()
	for _, want := range []string{
		"trust_domain=example.org",
		"source_type=bundle_endpoint",
		"sequence_present=true",
		"sequence=7",
		"x509_authority_count=1",
		"jwt_authority_count=1",
		"next_refresh_interval=5m0s",
		"last_success_age=",
		"error_class=fetch",
	} {
		require.Contains(t, output, want)
	}
	for _, secret := range []string{
		"sensitive-certificate",
		"sensitive-jwt-key",
		"sensitive-user",
		"sensitive-password",
		"sensitive raw bundle body",
		"BEGIN PUBLIC KEY",
	} {
		require.NotContains(t, output, secret)
	}
}

func TestSPIFFEBundleEndpointSourceDoesNotIncludeEndpointInErrors(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	source := newTestSPIFFEBundleEndpointSource(t, trustDomain, server)

	err := source.refresh(context.Background())
	require.Error(t, err)
	require.NotContains(t, fmt.Sprint(err), strings.TrimPrefix(server.URL, "https://"))
}
