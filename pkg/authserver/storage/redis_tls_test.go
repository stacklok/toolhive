// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	caCert, caPEM := generateTestCACert(t)

	tests := []struct {
		name    string
		cfg     *RedisTLSConfig
		wantErr bool
		check   func(t *testing.T, tc *tls.Config)
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
			name: "empty config returns basic TLS config",
			cfg:  &RedisTLSConfig{},
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
			cfg:  &RedisTLSConfig{InsecureSkipVerify: true},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				assert.True(t, tc.InsecureSkipVerify)
			},
		},
		{
			name: "custom CA cert",
			cfg:  &RedisTLSConfig{CACert: caPEM},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
				require.NotNil(t, tc.RootCAs)
				_, err := caCert.Verify(x509.VerifyOptions{Roots: tc.RootCAs})
				assert.NoError(t, err, "CA cert should be verifiable with the pool")
			},
		},
		{
			name:    "invalid CA cert returns error",
			cfg:     &RedisTLSConfig{CACert: []byte("not-a-cert")},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tc, err := buildTLSConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.check(t, tc)
		})
	}
}

func TestRedisTLSConfig_SeparateMasterAndSentinel(t *testing.T) {
	t.Parallel()

	masterCfg := &RedisTLSConfig{}
	sentinelCfg := &RedisTLSConfig{
		InsecureSkipVerify: true,
	}

	masterTLS, err := buildTLSConfig(masterCfg)
	require.NoError(t, err)
	sentinelTLS, err := buildTLSConfig(sentinelCfg)
	require.NoError(t, err)

	require.NotNil(t, masterTLS)
	require.NotNil(t, sentinelTLS)

	assert.False(t, masterTLS.InsecureSkipVerify, "master should verify certs")
	assert.True(t, sentinelTLS.InsecureSkipVerify, "sentinel should skip verification")
}

func TestNewTLSDialer(t *testing.T) {
	t.Parallel()

	timeout := 5 * time.Second

	t.Run("master TLS only: sentinel addr uses plaintext", func(t *testing.T) {
		t.Parallel()
		masterTLS := &tls.Config{MinVersion: tls.VersionTLS12}

		// Start a plaintext listener to simulate sentinel
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		localSentinelAddrs := []string{ln.Addr().String()}
		dialer := newTLSDialer(masterTLS, nil, localSentinelAddrs, timeout)
		require.NotNil(t, dialer)

		conn, err := dialer(context.Background(), "tcp", ln.Addr().String())
		require.NoError(t, err)
		conn.Close()
	})

	t.Run("sentinel TLS only: non-sentinel addr uses plaintext", func(t *testing.T) {
		t.Parallel()
		sentinelTLS := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // test
		sentinelAddrs := []string{"sentinel.example.com:26379"}

		// Start a plaintext listener to simulate master
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		dialer := newTLSDialer(nil, sentinelTLS, sentinelAddrs, timeout)
		require.NotNil(t, dialer)

		conn, err := dialer(context.Background(), "tcp", ln.Addr().String())
		require.NoError(t, err)
		conn.Close()
	})

	t.Run("sentinel address matching uses slices.Contains", func(t *testing.T) {
		t.Parallel()
		addrs := []string{"10.0.0.1:26379", "10.0.0.2:26379", "sentinel.redis.svc:26379"}

		// Start plaintext listener — not in sentinel list, so master config applies.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		sentinelTLS := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // test
		dialer := newTLSDialer(nil, sentinelTLS, addrs, timeout)

		conn, err := dialer(context.Background(), "tcp", ln.Addr().String())
		require.NoError(t, err)
		conn.Close()
	})
}

func TestConfigureTLSDialer(t *testing.T) {
	t.Parallel()

	_, caPEM := generateTestCACert(t)

	tests := []struct {
		name        string
		masterCfg   *RedisTLSConfig
		sentinelCfg *RedisTLSConfig
		wantDialer  bool
		wantErr     bool
	}{
		{
			name:       "no TLS configs — no dialer",
			wantDialer: false,
		},
		{
			name:       "master TLS only — installs dialer",
			masterCfg:  &RedisTLSConfig{},
			wantDialer: true,
		},
		{
			name:        "sentinel TLS only — installs dialer",
			sentinelCfg: &RedisTLSConfig{},
			wantDialer:  true,
		},
		{
			name:        "both TLS — installs dialer",
			masterCfg:   &RedisTLSConfig{},
			sentinelCfg: &RedisTLSConfig{InsecureSkipVerify: true},
			wantDialer:  true,
		},
		{
			name:      "master TLS with invalid CA cert — returns error",
			masterCfg: &RedisTLSConfig{CACert: []byte("bad-pem")},
			wantErr:   true,
		},
		{
			name:        "sentinel TLS with invalid CA cert — returns error",
			sentinelCfg: &RedisTLSConfig{CACert: []byte("bad-pem")},
			wantErr:     true,
		},
		{
			name:        "both TLS with valid CA certs — installs dialer",
			masterCfg:   &RedisTLSConfig{CACert: caPEM},
			sentinelCfg: &RedisTLSConfig{CACert: caPEM},
			wantDialer:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := &redis.FailoverOptions{
				SentinelAddrs: []string{"sentinel:26379"},
				DialTimeout:   5 * time.Second,
			}

			err := configureTLSDialer(opts, tt.masterCfg, tt.sentinelCfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.wantDialer {
				assert.NotNil(t, opts.Dialer, "expected custom dialer to be set")
			} else {
				assert.Nil(t, opts.Dialer, "expected no custom dialer")
			}
		})
	}
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
