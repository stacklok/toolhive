// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package redistls provides TLS fixtures for Redis tests: a TLS-only in-memory
// Redis endpoint verified by a test CA, and on-disk certificates for running a
// real Redis server with TLS.
package redistls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

// Certificate creates a self-signed CA certificate valid for the local Redis endpoint.
func Certificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Start runs a TLS-only Redis endpoint and writes its trusted certificate to a file.
func Start(t *testing.T) (*miniredis.Miniredis, string) {
	t.Helper()
	cert, ca := Certificate(t)
	server := miniredis.NewMiniRedis()
	require.NoError(t, server.StartTLS(&tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "redis-ca.pem")
	require.NoError(t, os.WriteFile(path, ca, 0600))
	return server, path
}

// Certificates are PEM files for a private CA and a server certificate signed
// by it. The server certificate is valid only for the IP address 127.0.0.1
// (no DNS names), so dialing the server as "localhost" must fail hostname
// verification.
type Certificates struct {
	// CACertFile is the CA a client must trust to complete the handshake.
	CACertFile string
	// ServerCertFile and ServerKeyFile are the server's certificate and key.
	ServerCertFile string
	ServerKeyFile  string

	server tls.Certificate
}

// NewCertificates generates a private CA and a 127.0.0.1 server certificate
// and writes them to dir as ca.crt, server.crt and server.key (mode 0600).
func NewCertificates(dir string) (*Certificates, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "redistls test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return nil, err
	}

	certs := &Certificates{
		CACertFile:     filepath.Join(dir, "ca.crt"),
		ServerCertFile: filepath.Join(dir, "server.crt"),
		ServerKeyFile:  filepath.Join(dir, "server.key"),
		server:         tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey},
	}
	files := map[string]*pem.Block{
		certs.CACertFile:     {Type: "CERTIFICATE", Bytes: caDER},
		certs.ServerCertFile: {Type: "CERTIFICATE", Bytes: srvDER},
		certs.ServerKeyFile:  {Type: "EC PRIVATE KEY", Bytes: srvKeyDER},
	}
	for path, block := range files {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return certs, nil
}

// StartWithCertificates runs a TLS-only Redis endpoint that presents the
// server certificate from certs.
func StartWithCertificates(t *testing.T, certs *Certificates) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.NewMiniRedis()
	require.NoError(t, server.StartTLS(&tls.Config{
		Certificates: []tls.Certificate{certs.server}, MinVersion: tls.VersionTLS12,
	}))
	t.Cleanup(server.Close)
	return server
}
