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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
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

func TestNewReadinessHTTPClient(t *testing.T) {
	t.Parallel()

	t.Run("accepts the configured certificate despite hostname mismatch", func(t *testing.T) {
		t.Parallel()

		material := writeTestTLSMaterial(t)
		server := startReadinessTLSServer(t, material, tls.VersionTLS12, 0)
		client, err := newReadinessHTTPClient(material)
		require.NoError(t, err)
		t.Cleanup(client.CloseIdleConnections)

		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("rejects an unconfigured certificate", func(t *testing.T) {
		t.Parallel()

		server := startReadinessTLSServer(t, writeTestTLSMaterial(t), tls.VersionTLS12, 0)
		client, err := newReadinessHTTPClient(writeTestTLSMaterial(t))
		require.NoError(t, err)
		t.Cleanup(client.CloseIdleConnections)

		_, err = client.Get(server.URL)
		require.ErrorContains(t, err, "peer certificate does not match configured serving certificate")
	})

	now := time.Now()
	validityTests := []struct {
		name       string
		notBefore  time.Time
		notAfter   time.Time
		wantErrMsg string
	}{
		{
			name:       "expired certificate",
			notBefore:  now.Add(-2 * time.Hour),
			notAfter:   now.Add(-time.Hour),
			wantErrMsg: "certificate has expired",
		},
		{
			name:       "not-yet-valid certificate",
			notBefore:  now.Add(time.Hour),
			notAfter:   now.Add(2 * time.Hour),
			wantErrMsg: "not yet valid",
		},
	}
	for _, tt := range validityTests {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()

			material := writeTestTLSMaterialWithValidity(t, tt.notBefore, tt.notAfter)
			server := startReadinessTLSServer(t, material, tls.VersionTLS12, 0)
			client, err := newReadinessHTTPClient(material)
			require.NoError(t, err)
			t.Cleanup(client.CloseIdleConnections)

			_, err = client.Get(server.URL)
			require.ErrorContains(t, err, tt.wantErrMsg)
		})
	}

	malformedTests := []struct {
		name        string
		corruptFile func(*TLSConfig) string
	}{
		{name: "certificate", corruptFile: func(config *TLSConfig) string { return config.CertFile }},
		{name: "private key", corruptFile: func(config *TLSConfig) string { return config.KeyFile }},
	}
	for _, tt := range malformedTests {
		t.Run("rejects malformed "+tt.name, func(t *testing.T) {
			t.Parallel()

			material := writeTestTLSMaterial(t)
			require.NoError(t, os.WriteFile(tt.corruptFile(material), []byte("not PEM"), 0o600))

			_, err := newReadinessHTTPClient(material)
			require.ErrorContains(t, err, "load readiness TLS certificate and key")
		})
	}

	t.Run("rejects TLS 1.1", func(t *testing.T) {
		t.Parallel()

		material := writeTestTLSMaterial(t)
		server := startReadinessTLSServer(t, material, tls.VersionTLS10, tls.VersionTLS11)
		client, err := newReadinessHTTPClient(material)
		require.NoError(t, err)
		t.Cleanup(client.CloseIdleConnections)

		_, err = client.Get(server.URL)
		require.Error(t, err)
	})
}

func startReadinessTLSServer(
	t *testing.T,
	material *TLSConfig,
	minVersion, maxVersion uint16,
) *httptest.Server {
	t.Helper()

	certificate, err := tls.LoadX509KeyPair(material.CertFile, material.KeyFile)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   minVersion,
		MaxVersion:   maxVersion,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func writeTestTLSMaterial(t *testing.T) *TLSConfig {
	t.Helper()

	now := time.Now()
	return writeTestTLSMaterialWithValidity(t, now.Add(-time.Minute), now.Add(time.Hour))
}

func writeTestTLSMaterialWithValidity(t *testing.T, notBefore, notAfter time.Time) *TLSConfig {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	certificateDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"toolhive.test.svc"},
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
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
