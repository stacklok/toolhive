// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

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
)

// generateSelfSignedCert creates a PEM-encoded self-signed certificate
// suitable for testing and returns the raw PEM bytes.
func generateSelfSignedCert(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ECDSA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
}

// writeTempFile writes data to a temporary file and registers cleanup.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid PEM certificate",
			setup: func(t *testing.T) string {
				t.Helper()
				certPEM := generateSelfSignedCert(t)
				return writeTempFile(t, "ca.pem", certPEM)
			},
			wantErr: false,
		},
		{
			name: "missing file",
			setup: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "nonexistent.pem")
			},
			wantErr:   true,
			errSubstr: "no such file",
		},
		{
			name: "invalid PEM data",
			setup: func(t *testing.T) string {
				t.Helper()
				return writeTempFile(t, "bad.pem", []byte("not a cert"))
			},
			wantErr:   true,
			errSubstr: "no valid certificates",
		},
		{
			name: "empty file",
			setup: func(t *testing.T) string {
				t.Helper()
				return writeTempFile(t, "empty.pem", []byte{})
			},
			wantErr:   true,
			errSubstr: "no valid certificates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.setup(t)
			cfg, err := buildTLSConfig(path)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstr != "" && !containsSubstring(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				if cfg != nil {
					t.Error("expected nil config on error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("expected non-nil TLS config")
			}
			if cfg.RootCAs == nil {
				t.Error("expected non-nil RootCAs in TLS config")
			}
		})
	}
}

// containsSubstring checks whether s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
