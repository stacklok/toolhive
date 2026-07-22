// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func TestBumpCA(t *testing.T) {
	t.Parallel()
	// CA-level dates use a fixed instant (rotation math is deterministic);
	// leaf minting uses real time because leaf validity (24h) must cover the
	// actual verification moment.
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	realNow := time.Now()

	t.Run("generation produces a valid self-signed CA", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("github-mcp-bump-ca", now)
		require.NoError(t, err)

		block, _ := pem.Decode(ca.CertPEM())
		require.NotNil(t, block)
		cert, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		assert.True(t, cert.IsCA)
		assert.Equal(t, "github-mcp-bump-ca", cert.Subject.CommonName)
		assert.WithinDuration(t, now.Add(egressbroker.CAValidity), cert.NotAfter, time.Minute)

		// Self-signed: verifies against its own pool.
		roots := x509.NewCertPool()
		roots.AddCert(cert)
		_, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
		require.NoError(t, err)
	})

	t.Run("PEM round-trip preserves signing ability", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("roundtrip", now)
		require.NoError(t, err)
		keyPEM, err := ca.KeyPEM()
		require.NoError(t, err)

		loaded, err := egressbroker.ParseBumpCA(ca.CertPEM(), keyPEM)
		require.NoError(t, err)

		// A leaf minted by the loaded CA must verify against the original's cert.
		leafPEM, _, err := loaded.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)
		assertLeafValidFor(t, ca.CertPEM(), leafPEM, "api.github.com")
	})

	t.Run("minted leaf verifies against the CA bundle for the SNI host", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("mint", now)
		require.NoError(t, err)
		leafPEM, _, err := ca.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)
		assertLeafValidFor(t, ca.CertPEM(), leafPEM, "api.github.com")
	})

	t.Run("leaf for one host does not verify for another", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("mint2", now)
		require.NoError(t, err)
		leafPEM, _, err := ca.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)

		block, _ := pem.Decode(leafPEM)
		require.NotNil(t, block)
		leaf, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		caBlock, _ := pem.Decode(ca.CertPEM())
		caCert, err := x509.ParseCertificate(caBlock.Bytes)
		require.NoError(t, err)
		roots := x509.NewCertPool()
		roots.AddCert(caCert)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "api.evil.com"})
		require.Error(t, err, "leaf must be bound to its SNI hostname")
	})

	t.Run("minting refuses empty hostname and IP literals (fail closed)", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("refuse", now)
		require.NoError(t, err)
		_, _, err = ca.MintLeaf("", now)
		require.Error(t, err)
		_, _, err = ca.MintLeaf("140.82.114.26", now)
		require.Error(t, err, "IP-literal SNI must not get a bump cert")
		_, _, err = ca.MintLeaf("::1", now)
		require.Error(t, err)
	})

	t.Run("rotation window: due at 50% validity", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("rotate", now)
		require.NoError(t, err)

		assert.False(t, ca.NeedsRotation(now), "fresh CA must not need rotation")
		assert.False(t, ca.NeedsRotation(now.Add(egressbroker.CAValidity/2-time.Hour)))
		assert.True(t, ca.NeedsRotation(now.Add(egressbroker.CAValidity/2)),
			"CA at 50% validity must be due for rotation")
		assert.True(t, ca.NeedsRotation(now.Add(egressbroker.CAValidity)),
			"expired CA must need rotation")
	})

	t.Run("rotation overlap: old and new CA each verify their own leaves", func(t *testing.T) {
		t.Parallel()
		oldCA, err := egressbroker.GenerateBumpCA("overlap", realNow.Add(-egressbroker.CAValidity/2))
		require.NoError(t, err)
		newCA, err := egressbroker.GenerateBumpCA("overlap", realNow)
		require.NoError(t, err)

		oldLeaf, _, err := oldCA.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)
		newLeaf, _, err := newCA.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)

		// Each pod trusts its own CA during the rollout overlap; there is no
		// cross-pod TLS, so cross-verification must fail.
		assertLeafValidFor(t, oldCA.CertPEM(), oldLeaf, "api.github.com")
		assertLeafValidFor(t, newCA.CertPEM(), newLeaf, "api.github.com")

		block, _ := pem.Decode(newLeaf)
		require.NotNil(t, block)
		leaf, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)
		oldBlock, _ := pem.Decode(oldCA.CertPEM())
		oldCert, err := x509.ParseCertificate(oldBlock.Bytes)
		require.NoError(t, err)
		oldRoots := x509.NewCertPool()
		oldRoots.AddCert(oldCert)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: oldRoots, DNSName: "api.github.com"})
		require.Error(t, err, "new-CA leaf must not verify against the old CA")
	})

	t.Run("parse rejects malformed and non-CA input (fail closed)", func(t *testing.T) {
		t.Parallel()
		ca, err := egressbroker.GenerateBumpCA("parse", now)
		require.NoError(t, err)
		keyPEM, err := ca.KeyPEM()
		require.NoError(t, err)

		_, err = egressbroker.ParseBumpCA([]byte("garbage"), keyPEM)
		require.Error(t, err)
		_, err = egressbroker.ParseBumpCA(ca.CertPEM(), []byte("garbage"))
		require.Error(t, err)
		// A leaf (non-CA) cert must not load as a CA.
		leafPEM, leafKey, err := ca.MintLeaf("api.github.com", realNow)
		require.NoError(t, err)
		_, err = egressbroker.ParseBumpCA(leafPEM, leafKey)
		require.Error(t, err, "non-CA cert must be rejected")
	})
}

func assertLeafValidFor(t *testing.T, caPEM, leafPEM []byte, dnsName string) { //nolint:unparam // varied call sites in prior revisions; kept parameterized for clarity
	t.Helper()
	caBlock, _ := pem.Decode(caPEM)
	require.NotNil(t, caBlock)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	require.NoError(t, err)
	leafBlock, _ := pem.Decode(leafPEM)
	require.NotNil(t, leafBlock)
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: dnsName})
	require.NoError(t, err, "leaf must verify against the CA bundle for %s", dnsName)
}
