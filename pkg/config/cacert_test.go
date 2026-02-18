// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validCACertificate = `-----BEGIN CERTIFICATE-----
MIIDfzCCAmegAwIBAgIUBE13KMDSoyh1O0x7PHpV/m0GW7kwDQYJKoZIhvcNAQEL
BQAwTzELMAkGA1UEBhMCVVMxDTALBgNVBAgMBFRlc3QxDTALBgNVBAcMBFRlc3Qx
EDAOBgNVBAoMB1Rlc3QgQ0ExEDAOBgNVBAMMB1Rlc3QgQ0EwHhcNMjUwNTI4MDYx
MTM3WhcNMjYwNTI4MDYxMTM3WjBPMQswCQYDVQQGEwJVUzENMAsGA1UECAwEVGVz
dDENMAsGA1UEBwwEVGVzdDEQMA4GA1UECgwHVGVzdCBDQTEQMA4GA1UEAwwHVGVz
dCBDQTCCASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAJqIW+I//m/8Yx1z
xdbi6ryHrqiFx07kqBW/RHdLtHD6jGGFuVtbUiKJIZotGmS6d458vU6oayMPXfGR
Vw1nTfWe0ZHKaNC9fnnFZw6nhaWDza7kYN0bhMCGNREqsU674/OTcbKHpIOMjszz
OdaymSyhiGBN1r7wpQS/D82W5L62Ol8f2jrk6CJR9wbQsVkTZkFYsivsINNgsBZ/
rvUxY0LeMZ70lFVWLAjoqias8QH0sjDPfVmHmmani3Vq5wdAdMJ8ZX0XdWhfpRoh
vbYEAnJno1/ao0Jj8kx+4a+vwnFGyUB6gGnR46/S/IyZTweQF60TSwaH2bA4MouF
Qnf9kuUCAwEAAaNTMFEwHQYDVR0OBBYEFHLsXlfUCBKrLdIOQYSKynA9qMALMB8G
A1UdIwQYMBaAFHLsXlfUCBKrLdIOQYSKynA9qMALMA8GA1UdEwEB/wQFMAMBAf8w
DQYJKoZIhvcNAQELBQADggEBAFPZYdu+HTuQdzZaE/0H2wnRbhXldisSMn4z9/3G
zO0LZifnzEtcbXIz2JTmsIVBOBovpjn70F8mR5+tNNMCdgATg6x82TXsu/ymJNV9
hJAGwEzF+U4gjlURVER25QqtPeKXrWVHmcSCYdcS0efpFfmY0tIeMDZvCMEZwk6j
oPRGpNavFD9NEMMVUhMggYk4LAqbaBFCQg2ON4yKkYXPnFe7ap2BWpM23sRBq58L
4CIV1qbg3fjbSxwLQjCN+T+FuucL9Jvswhyl/tCaFYPuMNamXBzLn0uObnjcjvkv
UukCUf8SUaaTa7XF7inVh8cJQYTO1w/QAMJePU6EcxR4Rkc=
-----END CERTIFICATE-----`

func TestCACertOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupCert    string // "valid", "invalid", "none", "deleted"
		useDirtyPath bool
		operation    string // "set", "get", "unset"
		wantErr      bool
		errContains  string
		checkResult  func(t *testing.T, provider Provider, certPath string)
	}{
		{
			name:      "set valid certificate",
			setupCert: "valid",
			operation: "set",
			checkResult: func(t *testing.T, provider Provider, certPath string) {
				t.Helper()
				assert.Equal(t, filepath.Clean(certPath), provider.GetConfig().CACertificatePath)
			},
		},
		{
			name:        "set nonexistent certificate",
			setupCert:   "none",
			operation:   "set",
			wantErr:     true,
			errContains: "CA certificate file not found or not accessible",
		},
		{
			name:        "set invalid certificate",
			setupCert:   "invalid",
			operation:   "set",
			wantErr:     true,
			errContains: "invalid CA certificate",
		},
		{
			name:         "set certificate with dirty path",
			setupCert:    "valid",
			useDirtyPath: true,
			operation:    "set",
			checkResult: func(t *testing.T, provider Provider, certPath string) {
				t.Helper()
				cfg := provider.GetConfig()
				assert.Equal(t, filepath.Clean(certPath), cfg.CACertificatePath)
				assert.NotContains(t, cfg.CACertificatePath, "..")
			},
		},
		{
			name:      "get existing certificate",
			setupCert: "valid",
			operation: "get",
			checkResult: func(t *testing.T, provider Provider, certPath string) {
				t.Helper()
				require.NoError(t, setCACert(provider, certPath))
				path, exists, accessible := getCACert(provider)
				assert.True(t, exists)
				assert.True(t, accessible)
				assert.Equal(t, filepath.Clean(certPath), path)
			},
		},
		{
			name:      "get when not set",
			setupCert: "none",
			operation: "get",
			checkResult: func(t *testing.T, provider Provider, _ string) {
				t.Helper()
				_, err := provider.LoadOrCreateConfig()
				require.NoError(t, err)
				path, exists, accessible := getCACert(provider)
				assert.False(t, exists)
				assert.False(t, accessible)
				assert.Equal(t, "", path)
			},
		},
		{
			name:      "get deleted certificate",
			setupCert: "deleted",
			operation: "get",
			checkResult: func(t *testing.T, provider Provider, certPath string) {
				t.Helper()
				path, exists, accessible := getCACert(provider)
				assert.True(t, exists)
				assert.False(t, accessible)
				assert.Equal(t, filepath.Clean(certPath), path)
			},
		},
		{
			name:      "unset existing certificate",
			setupCert: "valid",
			operation: "unset",
			checkResult: func(t *testing.T, provider Provider, certPath string) {
				t.Helper()
				require.NoError(t, setCACert(provider, certPath))
				require.NoError(t, unsetCACert(provider))
				assert.Equal(t, "", provider.GetConfig().CACertificatePath)
			},
		},
		{
			name:      "unset when not set",
			setupCert: "none",
			operation: "unset",
			checkResult: func(t *testing.T, provider Provider, _ string) {
				t.Helper()
				_, err := provider.LoadOrCreateConfig()
				require.NoError(t, err)
				require.NoError(t, unsetCACert(provider))
				assert.Equal(t, "", provider.GetConfig().CACertificatePath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "config.yaml")
			certPath := filepath.Join(tempDir, "test-ca.crt")
			provider := NewPathProvider(configPath)

			// Setup certificate file based on test case
			switch tt.setupCert {
			case "valid":
				require.NoError(t, os.WriteFile(certPath, []byte(validCACertificate), 0600))
			case "invalid":
				require.NoError(t, os.WriteFile(certPath, []byte("not a valid certificate"), 0600))
			case "deleted":
				require.NoError(t, os.WriteFile(certPath, []byte(validCACertificate), 0600))
				require.NoError(t, setCACert(provider, certPath))
				require.NoError(t, os.Remove(certPath))
			}

			// Execute operation
			var err error
			testPath := certPath
			if tt.useDirtyPath {
				testPath = certPath + "/../test-ca.crt"
			}

			switch tt.operation {
			case "set":
				err = setCACert(provider, testPath)
			case "unset":
				// Don't pre-set for unset tests, let checkResult handle it
			}

			// Check error expectations
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}

			// Run custom result checks
			if tt.checkResult != nil {
				tt.checkResult(t, provider, certPath)
			}
		})
	}
}

func TestProviderInterfaceCACert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider func(tempDir string) Provider
		expectOp bool // false for k8s no-ops
	}{
		{
			name: "PathProvider operations",
			provider: func(tempDir string) Provider {
				return NewPathProvider(filepath.Join(tempDir, "config.yaml"))
			},
			expectOp: true,
		},
		{
			name: "KubernetesProvider no-ops",
			provider: func(_ string) Provider {
				return NewKubernetesProvider()
			},
			expectOp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			certPath := filepath.Join(tempDir, "test-ca.crt")
			provider := tt.provider(tempDir)

			if tt.expectOp {
				require.NoError(t, os.WriteFile(certPath, []byte(validCACertificate), 0600))
			}

			// Set
			err := provider.SetCACert(certPath)
			assert.NoError(t, err)

			// Get
			path, exists, accessible := provider.GetCACert()
			if tt.expectOp {
				assert.True(t, exists)
				assert.True(t, accessible)
				assert.Equal(t, filepath.Clean(certPath), path)
			} else {
				assert.False(t, exists)
				assert.False(t, accessible)
				assert.Equal(t, "", path)
			}

			// Unset
			err = provider.UnsetCACert()
			assert.NoError(t, err)

			// Verify unset
			_, exists, _ = provider.GetCACert()
			assert.False(t, exists)
		})
	}
}
