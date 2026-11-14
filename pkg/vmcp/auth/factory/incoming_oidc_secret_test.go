// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
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

func TestNewOIDCAuthMiddleware_ClientSecretReachesIntrospection(t *testing.T) {
	oidcServer, _ := newTestOIDCServer(t)
	t.Cleanup(oidcServer.Close)

	var gotClientID, gotClientSecret string
	introspectionServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		gotClientID, gotClientSecret, ok = r.BasicAuth()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true,
			"sub":    "opaque-user",
			"iss":    oidcServer.URL,
			"aud":    "test-audience",
			"exp":    time.Now().Add(time.Hour).Unix(),
		})
	}))
	t.Cleanup(introspectionServer.Close)

	t.Setenv("VMCP_OIDC_CLIENT_SECRET", "secret-from-env")

	cfg := &config.OIDCConfig{
		Issuer:             oidcServer.URL,
		ClientID:           "test-client",
		Audience:           "test-audience",
		IntrospectionURL:   introspectionServer.URL,
		ClientSecretEnv:    "VMCP_OIDC_CLIENT_SECRET",
		CABundlePath:       writeTLSServerCert(t, introspectionServer),
		InsecureAllowHTTP:  true,
		JwksAllowPrivateIP: true,
	}

	authMw, _, err := newOIDCAuthMiddleware(t.Context(), cfg, nil, nil, "")
	require.NoError(t, err)
	require.NotNil(t, authMw)

	var gotSubject string
	handler := authMw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := pkgauth.IdentityFromContext(r.Context())
		require.True(t, ok)
		gotSubject = identity.Subject
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer opaque-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "test-client", gotClientID)
	assert.Equal(t, "secret-from-env", gotClientSecret)
	assert.Equal(t, "opaque-user", gotSubject)
}

func writeTLSServerCert(t *testing.T, server *httptest.Server) string {
	t.Helper()

	cert := server.Certificate()
	require.NotNil(t, cert)

	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600))
	return path
}
