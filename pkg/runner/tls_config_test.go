// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
)

func TestHasSPIFFEX509ClientAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		runConfig *authserver.RunConfig
		want      bool
	}{
		{name: "nil configuration"},
		{name: "no inbound grants", runConfig: &authserver.RunConfig{}},
		{
			name: "JWT association only",
			runConfig: &authserver.RunConfig{InboundGrants: &authserver.InboundGrantsRunConfig{
				SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
					Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodJWT},
				}},
			}},
		},
		{
			name: "X509 association",
			runConfig: &authserver.RunConfig{InboundGrants: &authserver.InboundGrantsRunConfig{
				SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
					Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
				}},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, hasSPIFFEX509ClientAuth(tt.runConfig))
		})
	}
}

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	t.Run("rejects missing or invalid certificate material", func(t *testing.T) {
		t.Parallel()

		invalidCert := filepath.Join(t.TempDir(), "cert.pem")
		require.NoError(t, os.WriteFile(invalidCert, []byte("not a certificate"), 0o600))

		tests := []struct {
			name string
			cfg  *TLSConfig
		}{
			{name: "nil configuration"},
			{name: "missing key file", cfg: &TLSConfig{CertFile: "cert.pem"}},
			{name: "invalid certificate", cfg: &TLSConfig{CertFile: invalidCert, KeyFile: invalidCert}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				_, err := buildTLSConfig(tt.cfg, false)
				require.Error(t, err)
			})
		}
	})

	for _, requestClientCertificate := range []bool{false, true} {
		requestClientCertificate := requestClientCertificate
		t.Run("loads certificate", func(t *testing.T) {
			t.Parallel()

			config := writeTestTLSMaterial(t)
			tlsConfig, err := buildTLSConfig(config, requestClientCertificate)
			require.NoError(t, err)
			assert.Equal(t, uint16(tls.VersionTLS12), tlsConfig.MinVersion)

			certificate, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{})
			require.NoError(t, err)
			require.NotNil(t, certificate)
			assert.NotEmpty(t, certificate.Certificate)

			wantClientAuth := tls.NoClientCert
			if requestClientCertificate {
				wantClientAuth = tls.RequestClientCert
			}
			assert.Equal(t, wantClientAuth, tlsConfig.ClientAuth)
		})
	}
}

func writeTestTLSMaterial(t *testing.T) *TLSConfig {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	certificateDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"localhost"},
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}, publicKey, privateKey)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600))
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}), 0o600))

	return &TLSConfig{CertFile: certFile, KeyFile: keyFile}
}
