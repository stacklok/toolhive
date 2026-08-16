// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTLSConfigClonesConfiguration(t *testing.T) {
	t.Parallel()

	original := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: "proxy.example.test",
	}
	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, "", nil, nil, nil, false, false, "sse", nil, nil, "", false, nil,
		WithTLSConfig(original),
	)
	require.NotNil(t, proxy.tlsConfig)
	assert.NotSame(t, original, proxy.tlsConfig)
	assert.Equal(t, original.MinVersion, proxy.tlsConfig.MinVersion)
	assert.Equal(t, original.ServerName, proxy.tlsConfig.ServerName)

	original.MinVersion = tls.VersionTLS13
	original.ServerName = "changed.example.test"
	assert.Equal(t, uint16(tls.VersionTLS12), proxy.tlsConfig.MinVersion)
	assert.Equal(t, "proxy.example.test", proxy.tlsConfig.ServerName)
}

func TestTransparentProxyTLSListenerAcceptsClientCertificate(t *testing.T) {
	t.Parallel()

	serverCertificate, serverLeaf := newShortLivedTLSCertificate(
		t, "proxy", []net.IP{net.ParseIP("127.0.0.1")}, x509.ExtKeyUsageServerAuth,
	)
	clientCertificate, _ := newShortLivedTLSCertificate(t, "client", nil, x509.ExtKeyUsageClientAuth)

	var handlerCalls atomic.Int32
	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, "", nil, nil,
		map[string]http.Handler{
			"/tls-test/": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalls.Add(1)
				if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
					http.Error(w, "client certificate not available", http.StatusInternalServerError)
					return
				}
				w.Header().Set("X-Client-Certificate-Subject", r.TLS.PeerCertificates[0].Subject.CommonName)
				w.WriteHeader(http.StatusOK)
			}),
		},
		false, false, "sse", nil, nil, "", false, nil,
		WithTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{serverCertificate},
			ClientAuth:   tls.RequestClientCert,
			MinVersion:   tls.VersionTLS12,
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		assert.NoError(t, proxy.Stop(stopCtx))
	})
	require.NoError(t, proxy.Start(ctx))

	roots := x509.NewCertPool()
	roots.AddCert(serverLeaf)
	clientTransport := &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientCertificate},
		MinVersion:   tls.VersionTLS12,
	}}
	t.Cleanup(clientTransport.CloseIdleConnections)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: clientTransport,
	}

	response, err := client.Get("https://" + proxy.ListenerAddr() + "/tls-test/request")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "client", response.Header.Get("X-Client-Certificate-Subject"))

	plainTransport := &http.Transport{}
	t.Cleanup(plainTransport.CloseIdleConnections)
	plainClient := &http.Client{Transport: plainTransport, Timeout: 5 * time.Second}
	plaintextResponse, plaintextErr := plainClient.Get("http://" + proxy.ListenerAddr() + "/tls-test/request")
	if plaintextErr == nil {
		require.NotNil(t, plaintextResponse)
		_, err = io.Copy(io.Discard, plaintextResponse.Body)
		require.NoError(t, err)
		require.NoError(t, plaintextResponse.Body.Close())
		assert.Equal(t, http.StatusBadRequest, plaintextResponse.StatusCode)
	}
	assert.Equal(t, int32(1), handlerCalls.Load(), "plaintext HTTP must not reach the TLS handler")
}

func newShortLivedTLSCertificate(
	t *testing.T,
	commonName string,
	ipAddresses []net.IP,
	extendedKeyUsage x509.ExtKeyUsage,
) (tls.Certificate, *x509.Certificate) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{extendedKeyUsage},
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey, Leaf: leaf}, leaf
}
