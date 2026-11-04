// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSPIFFEMultiDomainBundleSourceAuthorities(t *testing.T) {
	t.Parallel()

	x509Domain := spiffeid.RequireTrustDomainFromString("example.org")
	jwtDomain := spiffeid.RequireTrustDomainFromString("jwt.example.org")
	bundle := readTestBundle(t, x509Domain, testBundleDocument(t, 0))
	holder := &bundleHolder{}
	require.NoError(t, holder.store(bundle))
	source := &spiffeMultiDomainBundleSource{byTrustDomain: map[spiffeid.TrustDomain]*spiffeLiveBundleSource{
		x509Domain: {x509: holder, methods: map[SPIFFEAuthenticationMethod]struct{}{SPIFFEAuthenticationMethodX509: {}}},
		jwtDomain:  {x509: holder, methods: map[SPIFFEAuthenticationMethod]struct{}{SPIFFEAuthenticationMethodJWT: {}}},
	}}

	authorities := source.SPIFFEX509Authorities()
	require.Len(t, authorities, 1)
	assert.Equal(t, bundle.X509Authorities()[0].Raw, authorities[0].Raw)
	assert.Nil(t, (*spiffeMultiDomainBundleSource)(nil).SPIFFEX509Authorities())
}

func TestLoadSPIFFEBundleFileAcceptsSymlinksAndRejectsNonregularPaths(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "bundle.json")
	require.NoError(t, os.WriteFile(bundlePath, testBundleDocument(t, 0), 0o600))
	linkPath := filepath.Join(dir, "bundle-link.json")
	if err := os.Symlink(bundlePath, linkPath); err != nil {
		t.Skipf("symlinks may require special permissions on this platform: %v", err)
	}

	_, err := loadSPIFFEBundleFile(trustDomain, linkPath)
	require.NoError(t, err)

	_, err = loadSPIFFEBundleFile(trustDomain, dir)
	require.ErrorContains(t, err, "not a regular file")
}

func TestReloadSPIFFEBundleFileRotatesAndRetainsLastKnownGood(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	methods := map[SPIFFEAuthenticationMethod]struct{}{
		SPIFFEAuthenticationMethodX509: {},
		SPIFFEAuthenticationMethodJWT:  {},
	}
	path := writeTestBundleFile(t, testBundleDocument(t, 0))
	holder := &bundleHolder{}
	require.NoError(t, holder.store(readTestBundle(t, trustDomain, testBundleDocument(t, 0))))
	previous := holder.current.Load()

	rotatedDocument := testBundleDocumentWithSequence(t, 2, 0)
	require.NoError(t, os.WriteFile(path, rotatedDocument, 0o600))
	require.NoError(t, reloadSPIFFEBundleFile(trustDomain, path, holder, methods))
	rotated := holder.current.Load()
	require.NotSame(t, previous, rotated)
	sequence, ok := rotated.bundle.SequenceNumber()
	require.True(t, ok)
	require.Equal(t, uint64(2), sequence)

	require.NoError(t, os.WriteFile(path, []byte(`{"spiffe_sequence":3,"keys":[]}`), 0o600))
	require.ErrorContains(t, reloadSPIFFEBundleFile(trustDomain, path, holder, methods), "X.509 bundle has no authorities")
	assert.Same(t, rotated, holder.current.Load())

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	require.ErrorContains(t, reloadSPIFFEBundleFile(trustDomain, path, holder, methods), "not a regular file")
	assert.Same(t, rotated, holder.current.Load())
}

func TestReloadSPIFFEBundleFileRequiresAuthoritiesForEnabledMethods(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	for _, method := range []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT} {
		t.Run(string(method), func(t *testing.T) {
			t.Parallel()

			path := writeTestBundleFile(t, []byte(`{"spiffe_sequence":2,"keys":[]}`))
			holder := &bundleHolder{}
			previousBundle := readTestBundle(t, trustDomain, testBundleDocument(t, 0))
			require.NoError(t, holder.store(previousBundle))
			previous := holder.current.Load()

			err := reloadSPIFFEBundleFile(trustDomain, path, holder,
				map[SPIFFEAuthenticationMethod]struct{}{method: {}})
			require.ErrorContains(t, err, "no authorities")
			assert.Same(t, previous, holder.current.Load())
		})
	}
}

func TestNewSPIFFELiveBundleSourceLoadsFileBundle(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	path := writeTestBundleFile(t, testBundleDocument(t, 0))
	runtimeCtx, cancel := context.WithCancel(t.Context())
	multi := &spiffeMultiDomainBundleSource{cancel: cancel}

	live, err := newSPIFFELiveBundleSource(runtimeCtx, trustDomain, SPIFFETrustDomain{
		methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
		bundleSource: SPIFFEBundleSourceConfig{
			sourceType: SPIFFEBundleSourceTypeFile, path: path,
		},
	}, multi)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, multi.Close()) })
	assert.Empty(t, multi.closers, "a file source registers no external client to close")

	x509Bundle, err := live.x509.GetX509BundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	assert.Len(t, x509Bundle.X509Authorities(), 1)
	jwtBundle, err := live.jwt.GetJWTBundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	assert.Len(t, jwtBundle.JWTAuthorities(), 1)
}

func TestNewSPIFFELiveBundleSourceRejectsMissingFile(t *testing.T) {
	t.Parallel()

	multi := &spiffeMultiDomainBundleSource{}
	_, err := newSPIFFELiveBundleSource(t.Context(), spiffeid.RequireTrustDomainFromString("example.org"), SPIFFETrustDomain{
		methods: []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		bundleSource: SPIFFEBundleSourceConfig{
			sourceType: SPIFFEBundleSourceTypeFile, path: filepath.Join(t.TempDir(), "missing-bundle.json"),
		},
	}, multi)
	require.ErrorContains(t, err, "initial bundle load")
}

// TestNewSPIFFELiveBundleSourceRejectsUnsupportedSourceType locks in that a
// trust domain configured with a bundle source type this build does not
// implement (workload_api and bundle_endpoint are structurally valid CRD/
// RunConfig values but have no live loader in this build) fails closed with
// a clear error, rather than silently succeeding with no trust material.
func TestNewSPIFFELiveBundleSourceRejectsUnsupportedSourceType(t *testing.T) {
	t.Parallel()

	multi := &spiffeMultiDomainBundleSource{}
	_, err := newSPIFFELiveBundleSource(t.Context(), spiffeid.RequireTrustDomainFromString("example.org"), SPIFFETrustDomain{
		methods:      []SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509},
		bundleSource: SPIFFEBundleSourceConfig{sourceType: SPIFFEBundleSourceTypeWorkloadAPI},
	}, multi)
	require.ErrorContains(t, err, "unsupported SPIFFE bundle source type")
}

func TestValidateSPIFFEBundleAuthoritiesRejectsEmptyAuthorities(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	err := validateSPIFFEBundleAuthorities(spiffebundle.New(trustDomain), trustDomain,
		map[SPIFFEAuthenticationMethod]struct{}{SPIFFEAuthenticationMethodX509: {}})
	require.ErrorContains(t, err, "no authorities")
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

func TestSPIFFEMultiDomainBundleSourceCloseStopsFileWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	source := &spiffeMultiDomainBundleSource{cancel: cancel}
	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	path := writeTestBundleFile(t, testBundleDocument(t, 0))
	holder := &bundleHolder{}
	require.NoError(t, holder.store(readTestBundle(t, trustDomain, testBundleDocument(t, 0))))
	source.workers.Add(1)
	go pollFileBundle(ctx, trustDomain, path, holder,
		map[SPIFFEAuthenticationMethod]struct{}{SPIFFEAuthenticationMethodX509: {}}, &source.workers)

	done := make(chan error, 1)
	go func() { done <- source.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not stop file worker")
	}
}

func writeTestBundleFile(t *testing.T, document []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.json")
	require.NoError(t, os.WriteFile(path, document, 0o600))
	return path
}

func readTestBundle(t *testing.T, trustDomain spiffeid.TrustDomain, document []byte) *spiffebundle.Bundle {
	t.Helper()
	bundle, err := spiffebundle.Read(trustDomain, bytes.NewReader(document))
	require.NoError(t, err)
	return bundle
}

func testBundleDocument(t *testing.T, refreshHint int) []byte {
	t.Helper()
	return testBundleDocumentWithSequence(t, 1, refreshHint)
}

func testBundleDocumentWithSequence(t *testing.T, sequenceNumber, refreshHint int) []byte {
	t.Helper()
	key, der := testBundleAuthority(t)
	return []byte(fmt.Sprintf(`{"spiffe_sequence":%d,"spiffe_refresh_hint":%d,"keys":[{"kty":"RSA","kid":"test-x509","use":"x509-svid","x5c":["%s"],"n":"%s","e":"%s"},{"kty":"RSA","kid":"test-jwt","use":"jwt-svid","n":"%s","e":"%s"}]}`,
		sequenceNumber,
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
		testBundleAuthorityDER, testBundleAuthorityErr = x509.CreateCertificate(rand.Reader,
			&x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign},
			&x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign},
			&testBundleAuthorityKey.PublicKey, testBundleAuthorityKey)
	})
	require.NoError(t, testBundleAuthorityErr)
	return testBundleAuthorityKey, testBundleAuthorityDER
}
