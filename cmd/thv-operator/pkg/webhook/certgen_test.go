// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCertGenerator(t *testing.T) {
	t.Parallel()

	gen := NewCertGenerator("test-service", "test-namespace")
	assert.Equal(t, DefaultCertDir, gen.CertDir)
	assert.Equal(t, "test-service", gen.ServiceName)
	assert.Equal(t, "test-namespace", gen.Namespace)
}

func TestGenerate(t *testing.T) {
	t.Parallel()

	// Create temporary directory for certificates
	tempDir := t.TempDir()

	gen := &CertGenerator{
		CertDir:     tempDir,
		ServiceName: "test-webhook-service",
		Namespace:   "test-namespace",
	}

	// Generate certificates
	caBundle, err := gen.Generate()
	require.NoError(t, err)
	require.NotEmpty(t, caBundle)

	// Verify certificate file exists
	certPath := filepath.Join(tempDir, CertFileName)
	require.FileExists(t, certPath)

	// Verify key file exists
	keyPath := filepath.Join(tempDir, KeyFileName)
	require.FileExists(t, keyPath)

	// Verify certificate is valid PEM
	block, _ := pem.Decode(caBundle)
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE", block.Type)

	// Parse certificate
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	// Verify certificate properties
	assert.Equal(t, "test-webhook-service.test-namespace.svc", cert.Subject.CommonName)
	assert.True(t, cert.IsCA)
	assert.Contains(t, cert.DNSNames, "test-webhook-service")
	assert.Contains(t, cert.DNSNames, "test-webhook-service.test-namespace")
	assert.Contains(t, cert.DNSNames, "test-webhook-service.test-namespace.svc")
	assert.Contains(t, cert.DNSNames, "test-webhook-service.test-namespace.svc.cluster.local")

	// Verify key usage (includes CertSign for CA semantics)
	assert.Equal(t, x509.KeyUsageKeyEncipherment|x509.KeyUsageDigitalSignature|x509.KeyUsageCertSign, cert.KeyUsage)
	assert.Contains(t, cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth)

	// Verify certificate validity
	assert.False(t, cert.NotBefore.IsZero())
	assert.False(t, cert.NotAfter.IsZero())
	assert.True(t, cert.NotAfter.After(cert.NotBefore))
}

func TestCertificatesExist(t *testing.T) {
	t.Parallel()

	t.Run("no certificates exist", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir: tempDir,
		}

		assert.False(t, gen.CertificatesExist())
	})

	t.Run("only certificate exists", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir: tempDir,
		}

		// Create only certificate file
		certPath := filepath.Join(tempDir, CertFileName)
		err := os.WriteFile(certPath, []byte("test"), 0600)
		require.NoError(t, err)

		assert.False(t, gen.CertificatesExist())
	})

	t.Run("only key exists", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir: tempDir,
		}

		// Create only key file
		keyPath := filepath.Join(tempDir, KeyFileName)
		err := os.WriteFile(keyPath, []byte("test"), 0600)
		require.NoError(t, err)

		assert.False(t, gen.CertificatesExist())
	})

	t.Run("both certificate and key exist", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir:     tempDir,
			ServiceName: "test-service",
			Namespace:   "test-namespace",
		}

		// Generate certificates
		_, err := gen.Generate()
		require.NoError(t, err)

		assert.True(t, gen.CertificatesExist())
	})
}

func TestGetCABundle(t *testing.T) {
	t.Parallel()

	t.Run("certificate does not exist", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir: tempDir,
		}

		_, err := gen.GetCABundle()
		assert.Error(t, err)
	})

	t.Run("certificate exists and is valid", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir:     tempDir,
			ServiceName: "test-service",
			Namespace:   "test-namespace",
		}

		// Generate certificates
		originalBundle, err := gen.Generate()
		require.NoError(t, err)

		// Retrieve CA bundle
		retrievedBundle, err := gen.GetCABundle()
		require.NoError(t, err)
		assert.Equal(t, originalBundle, retrievedBundle)
	})

	t.Run("invalid certificate file", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir: tempDir,
		}

		// Create invalid certificate file
		certPath := filepath.Join(tempDir, CertFileName)
		err := os.WriteFile(certPath, []byte("not a valid certificate"), 0600)
		require.NoError(t, err)

		_, err = gen.GetCABundle()
		assert.Error(t, err)
	})
}

func TestEnsureCertificates(t *testing.T) {
	t.Parallel()

	t.Run("generates new certificates when none exist", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir:     tempDir,
			ServiceName: "test-service",
			Namespace:   "test-namespace",
		}

		assert.False(t, gen.CertificatesExist())

		caBundle, err := gen.EnsureCertificates()
		require.NoError(t, err)
		require.NotEmpty(t, caBundle)

		assert.True(t, gen.CertificatesExist())
	})

	t.Run("uses existing certificates", func(t *testing.T) {
		t.Parallel()

		tempDir := t.TempDir()
		gen := &CertGenerator{
			CertDir:     tempDir,
			ServiceName: "test-service",
			Namespace:   "test-namespace",
		}

		// Generate first set of certificates
		firstBundle, err := gen.EnsureCertificates()
		require.NoError(t, err)

		// Call EnsureCertificates again
		secondBundle, err := gen.EnsureCertificates()
		require.NoError(t, err)

		// Should return the same bundle
		assert.Equal(t, firstBundle, secondBundle)
	})
}

func TestEncodeCABundle(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "trims whitespace",
			input:    []byte("  test data  \n"),
			expected: []byte("test data"),
		},
		{
			name:     "handles empty input",
			input:    []byte(""),
			expected: nil, // TrimSpace returns nil for empty input
		},
		{
			name:     "preserves content",
			input:    []byte("test data"),
			expected: []byte("test data"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := EncodeCABundle(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGenerateWithInvalidDirectory(t *testing.T) {
	t.Parallel()

	gen := &CertGenerator{
		CertDir:     "/invalid/directory/that/cannot/be/created",
		ServiceName: "test-service",
		Namespace:   "test-namespace",
	}

	_, err := gen.Generate()
	assert.Error(t, err)
}
