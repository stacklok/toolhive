// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
)

type testCertificate struct {
	cert    *x509.Certificate
	certPEM []byte
	keyPEM  []byte
}

func makeTestCertificate(t *testing.T, commonName string) testCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return testCertificate{
		cert:    cert,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func writeTestCertificate(t *testing.T, dir, name string, pair testCertificate) (string, string) {
	t.Helper()
	certFile := filepath.Join(dir, name+".crt")
	keyFile := filepath.Join(dir, name+".key")
	require.NoError(t, os.WriteFile(certFile, pair.certPEM, 0600))
	require.NoError(t, os.WriteFile(keyFile, pair.keyPEM, 0600))
	return certFile, keyFile
}

func newTestTLSListener(t *testing.T, pair testCertificate, authorities func() []*x509.Certificate, requestClientCert bool) *tlsListener {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile := writeTestCertificate(t, dir, "server", pair)
	listener, err := newTLSListener("127.0.0.1", &authserver.TLSListenerRunConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	}, map[string]http.Handler{"/test": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}, authorities, requestClientCert)
	require.NoError(t, err)
	listener.port = 0
	return listener
}

func TestTLSListenerHTTPSAndClientAuth(t *testing.T) {
	t.Parallel()
	pair := makeTestCertificate(t, "server")

	for _, requestClientCert := range []bool{false, true} {
		t.Run(fmt.Sprintf("request_client_cert=%t", requestClientCert), func(t *testing.T) {
			listener := newTestTLSListener(t, pair, nil, requestClientCert)
			if requestClientCert {
				assert.Equal(t, tls.RequestClientCert, listener.server.TLSConfig.ClientAuth)
			} else {
				assert.Equal(t, tls.NoClientCert, listener.server.TLSConfig.ClientAuth)
			}
			require.NoError(t, listener.start())
			t.Cleanup(func() { require.NoError(t, listener.shutdown(context.Background())) })

			roots := x509.NewCertPool()
			require.True(t, roots.AppendCertsFromPEM(pair.certPEM))
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
			resp, err := client.Get("https://" + listener.listener.Addr().String() + "/test")
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, "ok", string(body))
		})
	}
}

func TestTLSListenerBindFailureAndStartTwice(t *testing.T) {
	t.Parallel()
	pair := makeTestCertificate(t, "server")
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })
	port := occupied.Addr().(*net.TCPAddr).Port

	listener := newTestTLSListener(t, pair, nil, false)
	listener.port = port
	err = listener.start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), occupied.Addr().String())

	listener.port = 0
	require.NoError(t, listener.start())
	t.Cleanup(func() { require.NoError(t, listener.shutdown(context.Background())) })
	assert.Error(t, listener.start())
}

func TestTLSListenerCAHintCache(t *testing.T) {
	t.Parallel()
	pair := makeTestCertificate(t, "server")
	authorityA := makeTestCertificate(t, "authority-a")
	authorityB := makeTestCertificate(t, "authority-b")
	authorities := []*x509.Certificate{authorityA.cert}
	listener := newTestTLSListener(t, pair, func() []*x509.Certificate { return authorities }, true)
	config, err := listener.server.TLSConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	require.NotNil(t, config.ClientCAs)
	assert.Equal(t, [][]byte{authorityA.cert.RawSubject}, config.ClientCAs.Subjects())
	same, err := listener.server.TLSConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	assert.Same(t, config.ClientCAs, same.ClientCAs)
	authorities = []*x509.Certificate{authorityB.cert}
	changed, err := listener.server.TLSConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	assert.NotSame(t, config.ClientCAs, changed.ClientCAs)
	assert.Equal(t, [][]byte{authorityB.cert.RawSubject}, changed.ClientCAs.Subjects())
}

func TestTLSListenerCertificateRotationKeepsLastGoodPair(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPair := makeTestCertificate(t, "old")
	newPair := makeTestCertificate(t, "new")
	certFile, keyFile := writeTestCertificate(t, dir, "server", oldPair)
	cache, err := newTLSKeyPairCache(certFile, keyFile)
	require.NoError(t, err)
	cache.checkInterval = 0

	cert, err := cache.getCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, oldPair.cert.Raw, cert.Certificate[0])
	require.NoError(t, os.WriteFile(certFile, newPair.certPEM, 0600))
	require.NoError(t, os.WriteFile(keyFile, newPair.keyPEM, 0600))
	cert, err = cache.getCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, newPair.cert.Raw, cert.Certificate[0])

	// A mismatched pair is retained as the last known-good certificate.
	require.NoError(t, os.WriteFile(certFile, oldPair.certPEM, 0600))
	require.NoError(t, os.WriteFile(keyFile, newPair.keyPEM, 0600))
	cert, err = cache.getCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, newPair.cert.Raw, cert.Certificate[0])
	require.NoError(t, os.WriteFile(keyFile, oldPair.keyPEM, 0600))
	cert, err = cache.getCertificate(nil)
	require.NoError(t, err)
	assert.Equal(t, oldPair.cert.Raw, cert.Certificate[0])
}

func TestTLSListenerShutdownAndEmbeddedStart(t *testing.T) {
	t.Parallel()
	pair := makeTestCertificate(t, "server")
	listener := newTestTLSListener(t, pair, nil, false)
	embedded := &EmbeddedAuthServer{server: &stubServer{}, tlsListener: listener, listenerHost: listener.host}
	require.NoError(t, embedded.Start())
	address := listener.listener.Addr().String()
	require.NoError(t, embedded.Close())
	require.NoError(t, embedded.Close())
	replacement, err := net.Listen("tcp", address)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())

	assert.NoError(t, (&EmbeddedAuthServer{}).Start())
	missingHost := &EmbeddedAuthServer{tlsListener: newTestTLSListener(t, pair, nil, false)}
	assert.ErrorContains(t, missingHost.Start(), "host")
}

func TestValidateTLSListenerIssuer(t *testing.T) {
	t.Parallel()
	pair := makeTestCertificate(t, "server")
	listener := newTestTLSListener(t, pair, nil, true)
	badScheme := &authserver.RunConfig{Issuer: "http://localhost", InboundGrants: &authserver.InboundGrantsRunConfig{SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{Methods: []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509}}}}}
	require.ErrorContains(t, validateTLSListenerIssuer(badScheme, listener.keyPair.leaf), "https")
	assert.NoError(t, validateTLSListenerIssuer(&authserver.RunConfig{Issuer: "https://example.com:443", InboundGrants: badScheme.InboundGrants}, listener.keyPair.leaf))
}

func TestTLSListenerConstructorRejectsInvalidCertificate(t *testing.T) {
	t.Parallel()
	_, err := newTLSListener("127.0.0.1", &authserver.TLSListenerRunConfig{CertFile: "missing", KeyFile: "missing"}, nil, nil, false)
	require.Error(t, err)
}
