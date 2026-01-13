// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package crypto

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSigningKey(t *testing.T) {
	t.Parallel()

	// Generate test keys once for the table
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	smallRSAKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, ed25519Key, _ := ed25519.GenerateKey(rand.Reader)

	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string) string // returns key path
		wantErr   string
		checkType func(t *testing.T, key any) // optional type check
	}{
		{
			name: "RSA PKCS1",
			setup: func(_ *testing.T, dir string) string {
				return writePEM(t, dir, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey))
			},
			checkType: func(t *testing.T, key any) { t.Helper(); assert.IsType(t, &rsa.PrivateKey{}, key) },
		},
		{
			name: "RSA PKCS8",
			setup: func(_ *testing.T, dir string) string {
				der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
				return writePEM(t, dir, "PRIVATE KEY", der)
			},
			checkType: func(t *testing.T, key any) { t.Helper(); assert.IsType(t, &rsa.PrivateKey{}, key) },
		},
		{
			name: "EC SEC1",
			setup: func(_ *testing.T, dir string) string {
				der, _ := x509.MarshalECPrivateKey(ecKey)
				return writePEM(t, dir, "EC PRIVATE KEY", der)
			},
			checkType: func(t *testing.T, key any) { t.Helper(); assert.IsType(t, &ecdsa.PrivateKey{}, key) },
		},
		{
			name: "EC PKCS8",
			setup: func(_ *testing.T, dir string) string {
				der, _ := x509.MarshalPKCS8PrivateKey(ecKey)
				return writePEM(t, dir, "PRIVATE KEY", der)
			},
			checkType: func(t *testing.T, key any) { t.Helper(); assert.IsType(t, &ecdsa.PrivateKey{}, key) },
		},
		{
			name: "Ed25519 PKCS8",
			setup: func(_ *testing.T, dir string) string {
				der, _ := x509.MarshalPKCS8PrivateKey(ed25519Key)
				return writePEM(t, dir, "PRIVATE KEY", der)
			},
			checkType: func(t *testing.T, key any) { t.Helper(); assert.IsType(t, ed25519.PrivateKey{}, key) },
		},
		{
			name: "RSA below minimum size PKCS1",
			setup: func(_ *testing.T, dir string) string {
				return writePEM(t, dir, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(smallRSAKey))
			},
			wantErr: "below minimum required",
		},
		{
			name: "RSA below minimum size PKCS8",
			setup: func(_ *testing.T, dir string) string {
				der, _ := x509.MarshalPKCS8PrivateKey(smallRSAKey)
				return writePEM(t, dir, "PRIVATE KEY", der)
			},
			wantErr: "below minimum required",
		},
		{
			name: "invalid PEM",
			setup: func(_ *testing.T, dir string) string {
				path := filepath.Join(dir, "key.pem")
				require.NoError(t, os.WriteFile(path, []byte("not valid PEM"), 0600))
				return path
			},
			wantErr: "failed to decode PEM block",
		},
		{
			name: "non-existent file",
			setup: func(_ *testing.T, _ string) string {
				return "/nonexistent/key.pem"
			},
			wantErr: "failed to read signing key",
		},
		{
			name: "invalid key data in PEM",
			setup: func(_ *testing.T, dir string) string {
				return writePEM(t, dir, "PRIVATE KEY", []byte("garbage"))
			},
			wantErr: "failed to parse signing key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			keyPath := tt.setup(t, t.TempDir())

			signer, err := LoadSigningKey(keyPath)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, signer)
			} else {
				require.NoError(t, err)
				require.NotNil(t, signer)
				if tt.checkType != nil {
					tt.checkType(t, signer)
				}
			}
		})
	}
}

func TestDeriveAlgorithm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     func() crypto.Signer
		wantAlg string
		wantErr bool
	}{
		{"RSA", func() crypto.Signer { k, _ := rsa.GenerateKey(rand.Reader, 2048); return k }, "RS256", false},
		{"EC P-256", func() crypto.Signer { k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader); return k }, "ES256", false},
		{"EC P-384", func() crypto.Signer { k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader); return k }, "ES384", false},
		{"EC P-521", func() crypto.Signer { k, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader); return k }, "ES512", false},
		{"Ed25519", func() crypto.Signer { _, k, _ := ed25519.GenerateKey(rand.Reader); return k }, "EdDSA", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			alg, err := DeriveAlgorithm(tt.key())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantAlg, alg)
			}
		})
	}
}

func TestValidateAlgorithmForKey(t *testing.T) {
	t.Parallel()

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecP256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecP384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	_, ed25519Key, _ := ed25519.GenerateKey(rand.Reader)

	tests := []struct {
		name    string
		alg     string
		key     crypto.Signer
		wantErr string
	}{
		// Valid combinations
		{"RS256 with RSA", "RS256", rsaKey, ""},
		{"RS384 with RSA", "RS384", rsaKey, ""},
		{"RS512 with RSA", "RS512", rsaKey, ""},
		{"ES256 with P-256", "ES256", ecP256, ""},
		{"ES384 with P-384", "ES384", ecP384, ""},
		{"EdDSA with Ed25519", "EdDSA", ed25519Key, ""},
		// Invalid combinations
		{"ES256 with RSA", "ES256", rsaKey, "not compatible with RSA"},
		{"RS256 with EC", "RS256", ecP256, "not compatible with EC"},
		{"ES256 with P-384", "ES256", ecP384, "not compatible with EC key"},
		{"RS256 with Ed25519", "RS256", ed25519Key, "not compatible with Ed25519"},
		{"ES256 with Ed25519", "ES256", ed25519Key, "not compatible with Ed25519"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAlgorithmForKey(tt.alg, tt.key)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDeriveSigningKeyParams(t *testing.T) {
	t.Parallel()

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, ed25519Key, _ := ed25519.GenerateKey(rand.Reader)

	tests := []struct {
		name      string
		key       crypto.Signer
		keyID     string
		algorithm string
		wantAlg   string
		wantErr   string
	}{
		{"derive both for RSA", rsaKey, "", "", "RS256", ""},
		{"derive both for EC", ecKey, "", "", "ES256", ""},
		{"derive both for Ed25519", ed25519Key, "", "", "EdDSA", ""},
		{"use provided values", rsaKey, "my-key", "RS384", "RS384", ""},
		{"derive alg only", ecKey, "my-key", "", "ES256", ""},
		{"invalid alg for RSA", rsaKey, "key", "ES256", "", "not compatible with RSA"},
		{"invalid alg for EC curve", ecKey, "key", "ES384", "", "not compatible with EC"},
		{"invalid alg for Ed25519", ed25519Key, "key", "RS256", "", "not compatible with Ed25519"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params, err := DeriveSigningKeyParams(tt.key, tt.keyID, tt.algorithm)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantAlg, params.Algorithm)
				if tt.keyID != "" {
					assert.Equal(t, tt.keyID, params.KeyID)
				} else {
					assert.NotEmpty(t, params.KeyID)
				}
			}
		})
	}
}

func TestDeriveKeyID(t *testing.T) {
	t.Parallel()

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	// Test consistency
	id1, err := DeriveKeyID(rsaKey)
	require.NoError(t, err)
	assert.NotEmpty(t, id1)

	id2, err := DeriveKeyID(rsaKey)
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "same key should produce same ID")

	// Test uniqueness
	rsaKey2, _ := rsa.GenerateKey(rand.Reader, 2048)
	id3, _ := DeriveKeyID(rsaKey2)
	assert.NotEqual(t, id1, id3, "different keys should produce different IDs")
}

func TestLoadHMACSecrets(t *testing.T) {
	t.Parallel()

	validSecret := strings.Repeat("a", 32)
	validSecret2 := strings.Repeat("b", 32)
	tooShortSecret := strings.Repeat("a", 31)

	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string) []string
		wantCurrent []byte
		wantRotated [][]byte
		wantErr     string
	}{
		{
			name:        "empty paths",
			setup:       func(_ *testing.T, _ string) []string { return []string{} },
			wantCurrent: nil,
			wantRotated: nil,
		},
		{
			name: "single secret",
			setup: func(_ *testing.T, dir string) []string {
				return []string{writeFileNamed(t, dir, "current", validSecret)}
			},
			wantCurrent: []byte(validSecret),
			wantRotated: nil,
		},
		{
			name: "with rotated secrets",
			setup: func(_ *testing.T, dir string) []string {
				return []string{
					writeFileNamed(t, dir, "current", validSecret),
					writeFileNamed(t, dir, "rotated1", validSecret2),
				}
			},
			wantCurrent: []byte(validSecret),
			wantRotated: [][]byte{[]byte(validSecret2)},
		},
		{
			name: "empty current path",
			setup: func(_ *testing.T, _ string) []string {
				return []string{""}
			},
			wantErr: "current HMAC secret path cannot be empty",
		},
		{
			name: "invalid current secret file",
			setup: func(_ *testing.T, _ string) []string {
				return []string{"/nonexistent/secret"}
			},
			wantErr: "failed to load current",
		},
		{
			name: "invalid rotated secret",
			setup: func(_ *testing.T, dir string) []string {
				return []string{
					writeFileNamed(t, dir, "current", validSecret),
					"/nonexistent/rotated",
				}
			},
			wantErr: "failed to load rotated HMAC secret [1]",
		},
		{
			name: "skip empty rotated paths",
			setup: func(_ *testing.T, dir string) []string {
				return []string{
					writeFileNamed(t, dir, "current", validSecret),
					"",
					writeFileNamed(t, dir, "rotated2", validSecret2),
				}
			},
			wantCurrent: []byte(validSecret),
			wantRotated: [][]byte{[]byte(validSecret2)},
		},
		{
			name: "whitespace trimmed",
			setup: func(_ *testing.T, dir string) []string {
				return []string{
					writeFileNamed(t, dir, "current", "  "+validSecret+"  \n\n"),
					writeFileNamed(t, dir, "rotated", "\t"+validSecret2+"\n"),
				}
			},
			wantCurrent: []byte(validSecret),
			wantRotated: [][]byte{[]byte(validSecret2)},
		},
		{
			name: "current too short",
			setup: func(_ *testing.T, dir string) []string {
				return []string{writeFileNamed(t, dir, "current", tooShortSecret)}
			},
			wantErr: "HMAC secret must be at least",
		},
		{
			name: "rotated too short",
			setup: func(_ *testing.T, dir string) []string {
				return []string{
					writeFileNamed(t, dir, "current", validSecret),
					writeFileNamed(t, dir, "rotated", tooShortSecret),
				}
			},
			wantErr: "failed to load rotated HMAC secret [1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			paths := tt.setup(t, dir)

			secrets, err := LoadHMACSecrets(paths)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, secrets)
			} else {
				require.NoError(t, err)
				if tt.wantCurrent == nil {
					assert.Nil(t, secrets)
				} else {
					require.NotNil(t, secrets)
					assert.Equal(t, tt.wantCurrent, secrets.Current)
					assert.Equal(t, tt.wantRotated, secrets.Rotated)
				}
			}
		})
	}
}

// Helpers

func writePEM(t *testing.T, dir, pemType string, der []byte) string {
	t.Helper()
	path := filepath.Join(dir, "key.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der})
	require.NoError(t, os.WriteFile(path, data, 0600))
	return path
}

func writeFileNamed(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}
