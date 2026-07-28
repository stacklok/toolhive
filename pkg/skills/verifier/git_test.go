// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	cms "github.com/github/smimesign/ietf-cms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreverifier "github.com/stacklok/toolhive-core/container/verifier"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

// fulcioIssuerOID is the Fulcio extension carrying the OIDC issuer
// (1.3.6.1.4.1.57264.1.1, the raw-string form gitsign certificates use).
var fulcioIssuerOID = []int{1, 3, 6, 1, 4, 1, 57264, 1, 1}

const testGitIssuer = "https://accounts.example.com"

// testCA is a synthetic Fulcio-style CA for signing test certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-fulcio-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key}
}

// issueSigningCert issues a short-lived Fulcio-style code-signing
// certificate with the given email SAN and OIDC issuer extension.
func (ca *testCA) issueSigningCert(t *testing.T, email string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(2),
		EmailAddresses: []string{email},
		// Fulcio certificates live for minutes.
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(9 * time.Minute),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: []pkix.Extension{
			{Id: fulcioIssuerOID, Value: []byte(testGitIssuer)},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func (ca *testCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// signCommitPayload produces the armored CMS detached signature gitsign
// attaches to commits.
func signCommitPayload(t *testing.T, payload []byte, cert *x509.Certificate, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := cms.SignDetached(payload, []*x509.Certificate{cert}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "SIGNED MESSAGE", Bytes: der})
}

const testCommitPayload = "tree 0123456789abcdef0123456789abcdef01234567\n" +
	"author Test <dev@example.com> 1700000000 +0000\n" +
	"committer Test <dev@example.com> 1700000000 +0000\n\n" +
	"Signed test commit\n"

func TestVerifyGitSignatureRoundTrip(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cert, key := ca.issueSigningCert(t, "dev@example.com")
	payload := []byte(testCommitPayload)
	sig := signCommitPayload(t, payload, cert, key)

	got, err := verifyGitSignature(t.Context(), payload, sig, ca.pool(), x509.NewCertPool())
	require.NoError(t, err, "a signature chaining to the trusted root must verify")
	assert.Equal(t, cert.SerialNumber, got.SerialNumber,
		"the authenticated certificate must be the signing leaf")

	// The identity extracted from the certificate carries the SAN and the
	// Fulcio issuer extension.
	identity, err := gitIdentityFromCertificate(got)
	require.NoError(t, err)
	assert.Equal(t, "dev@example.com", identity.SignerIdentity)
	assert.Equal(t, testGitIssuer, identity.CertIssuer)
}

func TestVerifyGitSignatureRejects(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cert, key := ca.issueSigningCert(t, "dev@example.com")
	payload := []byte(testCommitPayload)
	sig := signCommitPayload(t, payload, cert, key)

	t.Run("tampered payload", func(t *testing.T) {
		t.Parallel()
		tampered := []byte(testCommitPayload + "tampered\n")
		_, err := verifyGitSignature(t.Context(), tampered, sig, ca.pool(), x509.NewCertPool())
		require.Error(t, err, "a signature must not verify over modified commit content")
	})

	t.Run("untrusted root", func(t *testing.T) {
		t.Parallel()
		otherCA := newTestCA(t)
		_, err := verifyGitSignature(t.Context(), payload, sig, otherCA.pool(), x509.NewCertPool())
		require.Error(t, err, "a chain to an untrusted root must not verify")
	})

	t.Run("garbage signature", func(t *testing.T) {
		t.Parallel()
		_, err := verifyGitSignature(t.Context(), payload, []byte("not a signature"), ca.pool(), x509.NewCertPool())
		require.Error(t, err)
	})
}

func TestVerifyGitInputGuards(t *testing.T) {
	t.Parallel()
	d := NewDefault(nil)

	_, err := d.VerifyGit(t.Context(), []byte("payload"), nil, nil)
	require.ErrorIs(t, err, ErrUnsigned, "an empty signature is the unsigned case")

	_, err = d.VerifyGit(t.Context(), nil, []byte("sig"), nil)
	require.ErrorIs(t, err, ErrSignatureInvalid, "a signature without payload cannot verify")

	// A syntactically broken signature fails as invalid (the embedded
	// Fulcio pools load fine; parsing fails first).
	_, err = d.VerifyGit(t.Context(), []byte("payload"), []byte("garbage"), nil)
	require.ErrorIs(t, err, ErrSignatureInvalid)
}

func TestGitIdentityMatches(t *testing.T) {
	t.Parallel()
	base := coreverifier.Identity{SignerIdentity: "dev@example.com", CertIssuer: testGitIssuer}

	tests := []struct {
		name     string
		expected *lockfile.Provenance
		want     bool
	}{
		{
			name:     "exact match",
			expected: &lockfile.Provenance{SignerIdentity: "dev@example.com", CertIssuer: testGitIssuer},
			want:     true,
		},
		{
			name:     "identity mismatch",
			expected: &lockfile.Provenance{SignerIdentity: "other@example.com", CertIssuer: testGitIssuer},
			want:     false,
		},
		{
			name:     "issuer mismatch",
			expected: &lockfile.Provenance{SignerIdentity: "dev@example.com", CertIssuer: "https://other"},
			want:     false,
		},
		{
			name: "repository mismatch",
			expected: &lockfile.Provenance{
				SignerIdentity: "dev@example.com",
				CertIssuer:     testGitIssuer,
				RepositoryURI:  "https://github.com/org/repo",
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, gitIdentityMatches(base, tc.expected))
		})
	}
}

func TestVerifyGitEnforcesExpectedIdentity(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cert, key := ca.issueSigningCert(t, "dev@example.com")
	payload := []byte(testCommitPayload)
	sig := signCommitPayload(t, payload, cert, key)

	// Exercise the identity path through the internal helpers with the
	// test CA pools (VerifyGit itself pins the embedded Fulcio roots,
	// which a synthetic CA cannot chain to).
	got, err := verifyGitSignature(t.Context(), payload, sig, ca.pool(), x509.NewCertPool())
	require.NoError(t, err)
	identity, err := gitIdentityFromCertificate(got)
	require.NoError(t, err)

	assert.True(t, gitIdentityMatches(identity, &lockfile.Provenance{
		SignerIdentity: "dev@example.com",
		CertIssuer:     testGitIssuer,
	}))
	assert.False(t, gitIdentityMatches(identity, &lockfile.Provenance{
		SignerIdentity: "attacker@example.com",
		CertIssuer:     testGitIssuer,
	}))
}
