// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

type stubEnvReader map[string]string

func (s stubEnvReader) Getenv(key string) string {
	return s[key]
}

func (s stubEnvReader) LookupEnv(key string) (string, bool) {
	value, ok := s[key]
	return value, ok
}

func TestOIDCTokenValidatorConfig_ClientSecretEnv(t *testing.T) {
	t.Parallel()

	cfg := &config.OIDCConfig{
		Issuer:          "https://issuer.example.com",
		ClientID:        "test-client",
		Audience:        "test-audience",
		ClientSecretEnv: "VMCP_OIDC_CLIENT_SECRET",
	}

	got, err := oidcTokenValidatorConfig(cfg, stubEnvReader{
		"VMCP_OIDC_CLIENT_SECRET": "secret-from-env",
	})

	require.NoError(t, err)
	assert.Equal(t, "secret-from-env", got.ClientSecret)
	assert.Equal(t, "test-client", got.ClientID)
	assert.Equal(t, "test-audience", got.Audience)
}

func TestOIDCTokenValidatorConfig_MissingClientSecretEnv(t *testing.T) {
	t.Parallel()

	cfg := &config.OIDCConfig{
		ClientSecretEnv: "MISSING_VMCP_OIDC_CLIENT_SECRET",
	}

	_, err := oidcTokenValidatorConfig(cfg, stubEnvReader{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING_VMCP_OIDC_CLIENT_SECRET")
}

func TestOIDCTokenValidatorConfig_NoClientSecretEnv(t *testing.T) {
	t.Parallel()

	got, err := oidcTokenValidatorConfig(&config.OIDCConfig{}, stubEnvReader{})

	require.NoError(t, err)
	assert.Empty(t, got.ClientSecret)
}

func TestNewOIDCAuthMiddleware_MissingClientSecretEnv(t *testing.T) {
	t.Parallel()

	cfg := &config.OIDCConfig{
		Issuer:          "https://issuer.example.com",
		ClientID:        "test-client",
		Audience:        "test-audience",
		ClientSecretEnv: "MISSING_VMCP_OIDC_CLIENT_SECRET",
	}

	_, _, err := newOIDCAuthMiddleware(t.Context(), cfg, nil, nil, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING_VMCP_OIDC_CLIENT_SECRET")
}

func TestNewOIDCAuthMiddleware_ClientSecretEnv(t *testing.T) {
	server, _ := newTestOIDCServer(t)
	t.Cleanup(server.Close)

	t.Setenv("VMCP_OIDC_CLIENT_SECRET", "secret-from-env")

	cfg := &config.OIDCConfig{
		Issuer:             server.URL,
		ClientID:           "test-client",
		Audience:           "test-audience",
		ClientSecretEnv:    "VMCP_OIDC_CLIENT_SECRET",
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	authMw, _, err := newOIDCAuthMiddleware(t.Context(), cfg, nil, nil, "")

	require.NoError(t, err)
	require.NotNil(t, authMw)
}
