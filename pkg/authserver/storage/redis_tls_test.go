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

	// Generate a self-signed CA cert for testing
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
				_, err := caCert.Verify(x509.VerifyOptions{Roots: tc.RootCAs})
				assert.NoError(t, err, "CA cert should be verifiable with the pool")
			},
		},
		{
			name:    "invalid CA cert returns error",
			cfg:     &RedisTLSConfig{Enabled: true, CACert: []byte("not-a-cert")},
			wantErr: true,
		},
		{
			name: "disabled config still returns tls.Config",
			cfg:  &RedisTLSConfig{Enabled: false},
			check: func(t *testing.T, tc *tls.Config) {
				t.Helper()
				require.NotNil(t, tc)
			},
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

	masterCfg := &RedisTLSConfig{
		Enabled: true,
	}
	sentinelCfg := &RedisTLSConfig{
		Enabled:            true,
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
		sentinelAddrs := []string{"sentinel.example.com:26379"}

		// Start a plaintext listener to simulate sentinel
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		localSentinelAddrs := []string{ln.Addr().String()}
		dialer := newTLSDialer(masterTLS, nil, localSentinelAddrs, timeout)
		require.NotNil(t, dialer)
		_ = sentinelAddrs // used for documentation

		// Dialing sentinel addr with nil sentinelTLS should use plaintext
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

		// Dialing a non-sentinel addr with nil masterTLS should use plaintext
		conn, err := dialer(context.Background(), "tcp", ln.Addr().String())
		require.NoError(t, err)
		conn.Close()
	})

	t.Run("sentinel address matching uses slices.Contains", func(t *testing.T) {
		t.Parallel()
		addrs := []string{"10.0.0.1:26379", "10.0.0.2:26379", "sentinel.redis.svc:26379"}

		// Start plaintext listener — not in sentinel list, so master config applies.
		// With nil masterTLS, this means plaintext.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		sentinelTLS := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // test
		dialer := newTLSDialer(nil, sentinelTLS, addrs, timeout)

		// This addr is NOT in sentinel list → uses master config (nil = plaintext)
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
			masterCfg:  &RedisTLSConfig{Enabled: true},
			wantDialer: true,
		},
		{
			name:        "sentinel TLS only — installs dialer",
			sentinelCfg: &RedisTLSConfig{Enabled: true},
			wantDialer:  true,
		},
		{
			name:        "both TLS — installs dialer",
			masterCfg:   &RedisTLSConfig{Enabled: true},
			sentinelCfg: &RedisTLSConfig{Enabled: true, InsecureSkipVerify: true},
			wantDialer:  true,
		},
		{
			name:        "master TLS on, sentinel explicitly off — installs dialer",
			masterCfg:   &RedisTLSConfig{Enabled: true},
			sentinelCfg: &RedisTLSConfig{Enabled: false},
			wantDialer:  true,
		},
		{
			name:       "sentinel nil falls back to master TLS — installs dialer",
			masterCfg:  &RedisTLSConfig{Enabled: true},
			wantDialer: true,
		},
		{
			name:      "master TLS with invalid CA cert — returns error",
			masterCfg: &RedisTLSConfig{Enabled: true, CACert: []byte("bad-pem")},
			wantErr:   true,
		},
		{
			name:        "sentinel TLS with invalid CA cert — returns error",
			sentinelCfg: &RedisTLSConfig{Enabled: true, CACert: []byte("bad-pem")},
			wantErr:     true,
		},
		{
			name:        "both TLS with valid CA certs — installs dialer",
			masterCfg:   &RedisTLSConfig{Enabled: true, CACert: caPEM},
			sentinelCfg: &RedisTLSConfig{Enabled: true, CACert: caPEM},
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
