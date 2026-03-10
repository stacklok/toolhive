// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	// Generate a self-signed CA cert for testing
	caCert, caPEM := generateTestCACert(t)

	tests := []struct {
		name  string
		cfg   *RedisTLSConfig
		check func(t *testing.T, tc *tls.Config)
	}{
		{
			name: "nil config returns nil",
			cfg:  nil,
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				assert.Nil(t, tc)
			},
		},
		{
			name: "basic enabled config",
			cfg:  &RedisTLSConfig{Enabled: true},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				assert.Equal(t, uint16(tls.VersionTLS12), tc.MinVersion)
				assert.False(t, tc.InsecureSkipVerify)
				assert.Nil(t, tc.RootCAs)
			},
		},
		{
			name: "insecure skip verify",
			cfg:  &RedisTLSConfig{Enabled: true, InsecureSkipVerify: true},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				assert.True(t, tc.InsecureSkipVerify)
			},
		},
		{
			name: "custom CA cert",
			cfg:  &RedisTLSConfig{Enabled: true, CACert: caPEM},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				require.NotNil(t, tc.RootCAs)
				// Verify the pool contains our CA by checking if it can verify a cert signed by it
				_, err := caCert.Verify(x509.VerifyOptions{Roots: tc.RootCAs})
				assert.NoError(t, err, "CA cert should be verifiable with the pool")
			},
		},
		{
			name: "invalid CA cert is ignored (uses system CAs)",
			cfg:  &RedisTLSConfig{Enabled: true, CACert: []byte("not-a-cert")},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				// Pool is created but the invalid cert was not added;
				// the pool will be empty (no system CAs either since we created a new pool)
				require.NotNil(t, tc.RootCAs)
			},
		},
		{
			name: "disabled config still returns tls.Config",
			cfg:  &RedisTLSConfig{Enabled: false},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				// buildTLSConfig doesn't check Enabled — that's the caller's job
				require.NotNil(t, tc)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tc := buildTLSConfig(tt.cfg)
			tt.check(t, tc)
		})
	}
}

func TestRedisTLSConfig_SeparateMasterAndSentinel(t *testing.T) {
	t.Parallel()

	// Verify that separate configs can be created for master vs sentinel
	masterCfg := &RedisTLSConfig{
		Enabled: true,
		// Uses system CAs (e.g., Amazon Root CA for ElastiCache)
	}
	sentinelCfg := &RedisTLSConfig{
		Enabled:            true,
		InsecureSkipVerify: true, // self-signed sentinel cert
	}

	masterTLS := buildTLSConfig(masterCfg)
	sentinelTLS := buildTLSConfig(sentinelCfg)

	require.NotNil(t, masterTLS)
	require.NotNil(t, sentinelTLS)

	assert.False(t, masterTLS.InsecureSkipVerify, "master should verify certs")
	assert.True(t, sentinelTLS.InsecureSkipVerify, "sentinel should skip verification")
}

// generateTestCACert creates a self-signed CA certificate for testing.
func generateTestCACert(t *testing.T) (*x509.Certificate, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	return cert, certPEM
}
