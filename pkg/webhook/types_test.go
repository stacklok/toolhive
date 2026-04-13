// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	caBundle := filepath.Join(tmpDir, "ca.crt")
	clientCert := filepath.Join(tmpDir, "cert.pem")
	clientKey := filepath.Join(tmpDir, "key.pem")
	require.NoError(t, os.WriteFile(caBundle, []byte("dummy ca"), 0600))
	require.NoError(t, os.WriteFile(clientCert, []byte("dummy cert"), 0600))
	require.NoError(t, os.WriteFile(clientKey, []byte("dummy key"), 0600))

	validConfig := func() Config {
		return Config{
			Name:          "test-webhook",
			URL:           "https://example.com/webhook",
			Timeout:       5 * time.Second,
			FailurePolicy: FailurePolicyFail,
		}
	}

	tests := []struct {
		name          string
		modify        func(*Config)
		expectError   bool
		errorContains string
	}{
		{
			name:        "valid config with fail policy",
			modify:      func(_ *Config) {},
			expectError: false,
		},
		{
			name: "valid config with ignore policy",
			modify: func(c *Config) {
				c.FailurePolicy = FailurePolicyIgnore
			},
			expectError: false,
		},
		{
			name: "valid config with zero timeout sentinel",
			modify: func(c *Config) {
				c.Timeout = 0
			},
			expectError: false,
		},
		{
			name: "valid config with TLS",
			modify: func(c *Config) {
				c.TLSConfig = &TLSConfig{
					CABundlePath:   caBundle,
					ClientCertPath: clientCert,
					ClientKeyPath:  clientKey,
				}
			},
			expectError: false,
		},
		{
			name: "missing name",
			modify: func(c *Config) {
				c.Name = ""
			},
			expectError:   true,
			errorContains: "name is required",
		},
		{
			name: "missing URL",
			modify: func(c *Config) {
				c.URL = ""
			},
			expectError:   true,
			errorContains: "URL is required",
		},
		{
			name: "invalid URL",
			modify: func(c *Config) {
				c.URL = "not a url"
			},
			expectError:   true,
			errorContains: "URL is invalid",
		},
		{
			name: "invalid failure policy",
			modify: func(c *Config) {
				c.FailurePolicy = "invalid"
			},
			expectError:   true,
			errorContains: "failure_policy",
		},
		{
			name: "negative timeout",
			modify: func(c *Config) {
				c.Timeout = -1 * time.Second
			},
			expectError:   true,
			errorContains: "between 1s and 30s",
		},
		{
			name: "timeout exceeds max",
			modify: func(c *Config) {
				c.Timeout = MaxTimeout + time.Second
			},
			expectError:   true,
			errorContains: "exceeds maximum",
		},
		{
			name: "mTLS with only cert",
			modify: func(c *Config) {
				c.TLSConfig = &TLSConfig{
					ClientCertPath: "/path/to/cert.pem",
				}
			},
			expectError:   true,
			errorContains: "both client_cert_path and client_key_path",
		},
		{
			name: "mTLS with only key",
			modify: func(c *Config) {
				c.TLSConfig = &TLSConfig{
					ClientKeyPath: "/path/to/key.pem",
				}
			},
			expectError:   true,
			errorContains: "both client_cert_path and client_key_path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.modify(&cfg)

			err := cfg.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTypeConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Type("validating"), TypeValidating)
	assert.Equal(t, Type("mutating"), TypeMutating)
}

func TestFailurePolicyConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, FailurePolicy("fail"), FailurePolicyFail)
	assert.Equal(t, FailurePolicy("ignore"), FailurePolicyIgnore)
}
