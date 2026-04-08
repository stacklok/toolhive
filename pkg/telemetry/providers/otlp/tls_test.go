// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
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
)

// generateSelfSignedCACert creates a PEM-encoded self-signed CA certificate
// for use in tests.
func generateSelfSignedCACert(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"ToolHive Test CA"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
}

func TestNewTLSConfigFromCA(t *testing.T) {
	t.Parallel()

	// Create a valid CA cert file once for the subtests that need it.
	caCertPEM := generateSelfSignedCACert(t)
	validCertPath := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(validCertPath, caCertPEM, 0o600))

	// Create a file with invalid PEM content.
	invalidPEMPath := filepath.Join(t.TempDir(), "bad.crt")
	require.NoError(t, os.WriteFile(invalidPEMPath, []byte("not a cert"), 0o600))

	tests := []struct {
		name       string
		caCertPath string
		wantErr    bool
		errMsg     string
		validate   func(t *testing.T, cfg *tls.Config)
	}{
		{
			name:       "valid CA cert file",
			caCertPath: validCertPath,
			wantErr:    false,
			validate: func(t *testing.T, cfg *tls.Config) {
				t.Helper()
				assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
				assert.NotNil(t, cfg.RootCAs)
			},
		},
		{
			name:       "non-existent file",
			caCertPath: filepath.Join(t.TempDir(), "does-not-exist.crt"),
			wantErr:    true,
			errMsg:     "failed to read CA certificate bundle",
		},
		{
			name:       "invalid PEM content",
			caCertPath: invalidPEMPath,
			wantErr:    true,
			errMsg:     "no valid PEM certificates found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := newTLSConfigFromCA(tt.caCertPath)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cfg)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
				if tt.validate != nil {
					tt.validate(t, cfg)
				}
			}
		})
	}
}
