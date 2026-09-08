// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/networking"
)

var (
	testBundleAuthorityOnce sync.Once
	testBundleAuthorityKey  *rsa.PrivateKey
	testBundleAuthorityDER  []byte
	testBundleAuthorityErr  error
)

func TestSPIFFEMultiDomainBundleSourceDispatch(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	bundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
	holder := &bundleHolder{}
	require.NoError(t, holder.store(bundle))
	source := &spiffeMultiDomainBundleSource{byTrustDomain: map[spiffeid.TrustDomain]*spiffeLiveBundleSource{
		trustDomain: {x509: holder, jwt: holder, methods: map[SPIFFEAuthenticationMethod]struct{}{SPIFFEAuthenticationMethodX509: {}}},
	}}

	x509Bundle, err := source.GetX509BundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	assert.Len(t, x509Bundle.X509Authorities(), 1)

	_, err = source.GetJWTBundleForTrustDomain(trustDomain)
	require.ErrorContains(t, err, "is not enabled")
	_, err = source.GetX509BundleForTrustDomain(spiffeid.RequireTrustDomainFromString("other.org"))
	require.ErrorContains(t, err, "no SPIFFE bundle source configured")
}

func TestHTTPSWebBundleEndpointFetch(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	valid := testBundleDocument(t, 0)
	tests := []struct {
		name    string
		status  int
		body    []byte
		wantErr string
	}{
		{name: "success", status: http.StatusOK, body: valid},
		{name: "non-success status", status: http.StatusBadGateway, body: valid, wantErr: "HTTP 502"},
		{name: "oversized response", status: http.StatusOK, body: make([]byte, spiffeBundleMaxResponseSize+1), wantErr: "exceeds"},
		{name: "malformed bundle", status: http.StatusOK, body: []byte("not JSON"), wantErr: "read SPIFFE bundle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(server.Close)

			endpoint := newTestHTTPSWebBundleEndpoint(t, trustDomain, server)
			bundle, err := endpoint.fetch(t.Context())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, bundle)
				return
			}
			require.NoError(t, err)
			assert.Len(t, bundle.X509Authorities(), 1)
		})
	}
}

func TestHTTPSWebBundleEndpointRejectsCrossHostRedirect(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("cross-host redirect was followed")
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	endpoint := newTestHTTPSWebBundleEndpoint(t, spiffeid.RequireTrustDomainFromString("example.org"), redirector)
	_, err := endpoint.fetch(t.Context())
	require.Error(t, err)
}

func newTestHTTPSWebBundleEndpoint(
	t *testing.T, trustDomain spiffeid.TrustDomain, server *httptest.Server,
) *httpsWebBundleEndpoint {
	t.Helper()
	endpoint, err := newHTTPSWebBundleEndpoint(trustDomain, server.URL)
	require.NoError(t, err)
	transport, ok := endpoint.client.Transport.(*networking.ValidatingTransport)
	require.True(t, ok)
	baseTransport, ok := transport.Transport.(*http.Transport)
	require.True(t, ok)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	baseTransport.TLSClientConfig = &tls.Config{RootCAs: roots}
	return endpoint
}

func TestBundleHolderFailsClosedAfterStaleSnapshotAndRecovers(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	staleBundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
	freshBundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
	holder := &bundleHolder{}
	holder.current.Store(&bundleSnapshot{
		bundle:    staleBundle,
		fetchedAt: time.Now().Add(-spiffeBundleMaxStaleAge - time.Nanosecond),
	})

	_, err := holder.GetX509BundleForTrustDomain(trustDomain)
	require.ErrorContains(t, err, "older than")
	_, err = holder.GetJWTBundleForTrustDomain(trustDomain)
	require.ErrorContains(t, err, "older than")

	require.NoError(t, holder.store(freshBundle))
	_, err = holder.GetX509BundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	_, err = holder.GetJWTBundleForTrustDomain(trustDomain)
	require.NoError(t, err)
}

func TestBundleHolderRejectsSequenceReplays(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	current := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
	original := &bundleSnapshot{bundle: current, fetchedAt: time.Now().Add(-time.Hour)}
	tests := []struct {
		name      string
		candidate func(*testing.T) *spiffebundle.Bundle
		wantErr   string
	}{
		{
			name: "missing sequence",
			candidate: func(t *testing.T) *spiffebundle.Bundle {
				t.Helper()
				bundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
				bundle.ClearSequenceNumber()
				return bundle
			},
			wantErr: "omits sequence number",
		},
		{
			name: "lower sequence",
			candidate: func(t *testing.T) *spiffebundle.Bundle {
				t.Helper()
				bundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
				bundle.SetSequenceNumber(0)
				return bundle
			},
			wantErr: "lower than current sequence",
		},
		{
			name: "conflicting equal sequence",
			candidate: func(t *testing.T) *spiffebundle.Bundle {
				t.Helper()
				return readTestBundle(t, trustDomain, testBundleDocument(t, 1))
			},
			wantErr: "conflicts with the current bundle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			holder := &bundleHolder{}
			holder.current.Store(original)

			require.ErrorContains(t, holder.store(tt.candidate(t)), tt.wantErr)
			assert.Same(t, original, holder.current.Load())
		})
	}
}

func TestBundleHolderRefreshesEqualSequenceAndContent(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	current := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
	original := &bundleSnapshot{bundle: current, fetchedAt: time.Now().Add(-time.Hour)}
	holder := &bundleHolder{}
	holder.current.Store(original)

	require.NoError(t, holder.store(readTestBundle(t, trustDomain, testBundleDocument(t, 0))))
	snapshot := holder.current.Load()
	assert.NotSame(t, original, snapshot)
	assert.True(t, snapshot.bundle.Equal(current))
	assert.True(t, snapshot.fetchedAt.After(original.fetchedAt))
}

func TestSPIFFEBundleRefreshInterval(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	tests := []struct {
		name string
		hint int
		want time.Duration
	}{
		{name: "absent hint uses default", want: spiffeBundleDefaultRefresh},
		{name: "short hint is clamped", hint: 1, want: spiffeBundleMinimumRefresh},
		{name: "long hint is retained", hint: 90, want: 90 * time.Second},
		{name: "oversized hint is capped", hint: int(spiffeBundleMaxStaleAge / time.Second * 2), want: spiffeBundleMaxStaleAge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := readTestBundle(t, trustDomain, testBundleDocument(t, tt.hint))
			assert.Equal(t, tt.want, refreshInterval(bundle))
		})
	}
}

func TestSPIFFEMultiDomainBundleSourceCloseStopsEndpointWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	source := &spiffeMultiDomainBundleSource{cancel: cancel}
	endpoint := &httpsWebBundleEndpoint{}
	holder := &bundleHolder{}
	require.NoError(t, holder.store(readTestBundle(t, spiffeid.RequireTrustDomainFromString("example.org"), testBundleDocument(t, 0))))
	source.workers.Add(1)
	go endpoint.poll(ctx, holder, &source.workers)

	done := make(chan error, 1)
	go func() { done <- source.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not stop endpoint worker")
	}
}

func TestNewSPIFFELiveBundleSourceRejectsHTTPSSPIFFE(t *testing.T) {
	t.Parallel()

	multi := &spiffeMultiDomainBundleSource{}
	_, err := newSPIFFELiveBundleSource(t.Context(), t.Context(), spiffeid.RequireTrustDomainFromString("example.org"), SPIFFETrustDomain{
		methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		bundleSource: SPIFFEBundleSourceConfig{sourceType: SPIFFEBundleSourceTypeEndpoint, profile: SPIFFEBundleEndpointProfileHTTPSSPIFFE},
	}, multi, nil)
	require.ErrorContains(t, err, "https_spiffe bundle endpoints are not supported")
}

func readTestBundle(t *testing.T, trustDomain spiffeid.TrustDomain, document []byte) *spiffebundle.Bundle {
	t.Helper()
	bundle, err := spiffebundle.Read(trustDomain, bytes.NewReader(document))
	require.NoError(t, err)
	return bundle
}

func testBundleDocument(t *testing.T, refreshHint int) []byte {
	t.Helper()
	key, der := testBundleAuthority(t)
	return []byte(fmt.Sprintf(`{"spiffe_sequence":1,"spiffe_refresh_hint":%d,"keys":[{"kty":"RSA","kid":"test-x509","use":"x509-svid","x5c":["%s"],"n":"%s","e":"%s"},{"kty":"RSA","kid":"test-jwt","use":"jwt-svid","n":"%s","e":"%s"}]}`,
		refreshHint,
		base64.StdEncoding.EncodeToString(der),
		base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	))
}

func testBundleAuthority(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	testBundleAuthorityOnce.Do(func() {
		testBundleAuthorityKey, testBundleAuthorityErr = rsa.GenerateKey(rand.Reader, 2048)
		if testBundleAuthorityErr != nil {
			return
		}
		testBundleAuthorityDER, testBundleAuthorityErr = x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, &testBundleAuthorityKey.PublicKey, testBundleAuthorityKey)
	})
	require.NoError(t, testBundleAuthorityErr)
	return testBundleAuthorityKey, testBundleAuthorityDER
}
