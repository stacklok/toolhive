// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestNewOIDCAuthMiddleware_CABundlePath verifies that the CABundlePath field on
// OIDCConfig is wired into the underlying auth.TokenValidatorConfig. A bad path
// is expected to surface as an error at middleware construction time, and a
// valid PEM bundle is expected to succeed — proving the field is actually
// consumed (not silently dropped) by the downstream HTTP client builder.
func TestNewOIDCAuthMiddleware_CABundlePath(t *testing.T) {
	t.Parallel()

	// Stand up a minimal OIDC discovery server so the validator has an issuer
	// to talk to during construction. The CA bundle affects the TLS trust
	// store but not issuer reachability here.
	server, _ := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	validCAPath := writeTestCAPEM(t)

	tests := []struct {
		name         string
		caBundlePath string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "empty caBundlePath succeeds (backward compatible)",
			caBundlePath: "",
			wantErr:      false,
		},
		{
			name:         "valid PEM caBundlePath is loaded successfully",
			caBundlePath: validCAPath,
			wantErr:      false,
		},
		{
			name:         "non-existent caBundlePath surfaces as an error",
			caBundlePath: "/nonexistent/ca/bundle.pem",
			wantErr:      true,
			errContains:  "CA certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			oidcCfg := &config.OIDCConfig{
				Issuer:             server.URL,
				ClientID:           "test-client",
				Audience:           "test-audience",
				InsecureAllowHTTP:  true,
				JwksAllowPrivateIP: true,
				CABundlePath:       tt.caBundlePath,
			}

			authMw, _, err := newOIDCAuthMiddleware(t.Context(), oidcCfg, nil, nil)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, authMw)
		})
	}
}

// writeTestCAPEM generates a minimal self-signed certificate and writes it as
// PEM into t.TempDir(). Returns the absolute path to the PEM file. The cert
// content does not need to match any issuer — the test only exercises that
// the bundle loads successfully into the HTTP client's trust store.
func writeTestCAPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "toolhive-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NotEmpty(t, pemBytes)

	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
	return path
}
