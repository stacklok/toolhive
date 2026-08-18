// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/require"
)

func TestSPIFFEBundleFileSourceLoadsAndRetainsLastKnownGoodBundle(t *testing.T) {
	t.Parallel()

	trustDomain := spiffeid.RequireTrustDomainFromString("example.org")
	path := filepath.Join(t.TempDir(), "bundle.json")
	initial := testSPIFFEBundleWithAuthorities(t, trustDomain, 1, "x509", "jwt")
	writeSPIFFEBundleFile(t, path, initial)

	source, err := newSPIFFEBundleFileSource(
		trustDomain,
		[]SPIFFEAuthenticationMethod{SPIFFEAuthenticationMethodX509, SPIFFEAuthenticationMethodJWT},
		path,
		time.Hour,
	)
	require.NoError(t, err)
	require.NoError(t, source.start(context.Background()))
	t.Cleanup(source.close)

	x509Bundle, err := source.GetX509BundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	require.Len(t, x509Bundle.X509Authorities(), 1)
	jwtBundle, err := source.GetJWTBundleForTrustDomain(trustDomain)
	require.NoError(t, err)
	require.Len(t, jwtBundle.JWTAuthorities(), 1)

	current := source.bundle.Load()
	next := testSPIFFEBundleWithAuthorities(t, trustDomain, 2, "x509", "jwt")
	writeSPIFFEBundleFile(t, path, next)
	require.NoError(t, source.refresh())
	require.NotSame(t, current, source.bundle.Load())

	incomplete := testSPIFFEBundleWithAuthorities(t, trustDomain, 3, "x509", "")
	writeSPIFFEBundleFile(t, path, incomplete)
	require.ErrorContains(t, source.refresh(), "no JWT authorities")
	require.Same(t, next, source.bundle.Load())

	require.NoError(t, os.WriteFile(path, []byte("not a trust bundle"), 0600))
	require.Error(t, source.refresh())
	require.Same(t, next, source.bundle.Load())
}

func writeSPIFFEBundleFile(t *testing.T, path string, bundle any) {
	t.Helper()
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0600))
}
