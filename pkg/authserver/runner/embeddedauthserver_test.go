// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
)

func TestCreateKeyProvider(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns GeneratingProvider", func(t *testing.T) {
		t.Parallel()

		provider, err := createKeyProvider(nil)
		require.NoError(t, err)
		require.NotNil(t, provider)

		// GeneratingProvider should return a key when asked
		_, ok := provider.(*keys.GeneratingProvider)
		assert.True(t, ok, "expected GeneratingProvider")
	})

	t.Run("empty SigningKeyFile returns GeneratingProvider", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.SigningKeyRunConfig{
			KeyDir:         "/some/dir",
			SigningKeyFile: "",
		}

		provider, err := createKeyProvider(cfg)
		require.NoError(t, err)
		require.NotNil(t, provider)

		_, ok := provider.(*keys.GeneratingProvider)
		assert.True(t, ok, "expected GeneratingProvider")
	})

	t.Run("valid config creates FileProvider", func(t *testing.T) {
		t.Parallel()

		// Create a temporary directory with a test key
		tmpDir := t.TempDir()
		keyFile := "test-key.pem"

		// Generate a test EC P-256 key and encode it in SEC 1 (EC PRIVATE KEY) format
		ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		ecBytes, err := x509.MarshalECPrivateKey(ecKey)
		require.NoError(t, err)

		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: ecBytes,
		})

		err = os.WriteFile(filepath.Join(tmpDir, keyFile), keyPEM, 0600)
		require.NoError(t, err)

		cfg := &authserver.SigningKeyRunConfig{
			KeyDir:         tmpDir,
			SigningKeyFile: keyFile,
		}

		provider, err := createKeyProvider(cfg)
		require.NoError(t, err)
		require.NotNil(t, provider)

		_, ok := provider.(*keys.FileProvider)
		assert.True(t, ok, "expected FileProvider")
	})

	t.Run("missing key file returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.SigningKeyRunConfig{
			KeyDir:         "/nonexistent",
			SigningKeyFile: "missing.pem",
		}

		_, err := createKeyProvider(cfg)
		require.Error(t, err)
	})
}

func TestLoadHMACSecrets(t *testing.T) {
	t.Parallel()

	t.Run("empty files returns nil (development mode)", func(t *testing.T) {
		t.Parallel()

		secrets, err := loadHMACSecrets(nil)
		require.NoError(t, err)
		assert.Nil(t, secrets)

		secrets, err = loadHMACSecrets([]string{})
		require.NoError(t, err)
		assert.Nil(t, secrets)
	})

	t.Run("single file loads current secret", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "hmac-secret")
		secretValue := "this-is-a-secret-that-is-at-least-32-bytes-long"

		err := os.WriteFile(secretFile, []byte(secretValue), 0600)
		require.NoError(t, err)

		secrets, err := loadHMACSecrets([]string{secretFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		assert.Equal(t, []byte(secretValue), secrets.Current)
		assert.Empty(t, secrets.Rotated)
	})

	t.Run("multiple files load current and rotated secrets", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		currentFile := filepath.Join(tmpDir, "hmac-current")
		rotatedFile := filepath.Join(tmpDir, "hmac-rotated")

		currentSecret := "current-secret-that-is-at-least-32-bytes-long"
		rotatedSecret := "rotated-secret-that-is-at-least-32-bytes-long"

		require.NoError(t, os.WriteFile(currentFile, []byte(currentSecret), 0600))
		require.NoError(t, os.WriteFile(rotatedFile, []byte(rotatedSecret), 0600))

		secrets, err := loadHMACSecrets([]string{currentFile, rotatedFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		assert.Equal(t, []byte(currentSecret), secrets.Current)
		require.Len(t, secrets.Rotated, 1)
		assert.Equal(t, []byte(rotatedSecret), secrets.Rotated[0])
	})

	t.Run("trims whitespace from secrets", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "hmac-secret")
		secretValue := "  secret-with-whitespace  \n"

		err := os.WriteFile(secretFile, []byte(secretValue), 0600)
		require.NoError(t, err)

		secrets, err := loadHMACSecrets([]string{secretFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		assert.Equal(t, []byte("secret-with-whitespace"), secrets.Current)
	})

	t.Run("skips empty paths in rotated files", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		currentFile := filepath.Join(tmpDir, "hmac-current")
		rotatedFile := filepath.Join(tmpDir, "hmac-rotated")

		require.NoError(t, os.WriteFile(currentFile, []byte("current-secret-32-bytes-minimum!"), 0600))
		require.NoError(t, os.WriteFile(rotatedFile, []byte("rotated-secret-32-bytes-minimum!"), 0600))

		secrets, err := loadHMACSecrets([]string{currentFile, "", rotatedFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		require.Len(t, secrets.Rotated, 1)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		t.Parallel()

		_, err := loadHMACSecrets([]string{"/nonexistent/file"})
		require.Error(t, err)
	})
}

func TestParseTokenLifespans(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns zero values", func(t *testing.T) {
		t.Parallel()

		access, refresh, authCode, err := parseTokenLifespans(nil)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), access)
		assert.Equal(t, time.Duration(0), refresh)
		assert.Equal(t, time.Duration(0), authCode)
	})

	t.Run("empty config returns zero values", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.TokenLifespanRunConfig{}
		access, refresh, authCode, err := parseTokenLifespans(cfg)
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), access)
		assert.Equal(t, time.Duration(0), refresh)
		assert.Equal(t, time.Duration(0), authCode)
	})

	t.Run("parses valid durations", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.TokenLifespanRunConfig{
			AccessTokenLifespan:  "1h",
			RefreshTokenLifespan: "168h",
			AuthCodeLifespan:     "10m",
		}

		access, refresh, authCode, err := parseTokenLifespans(cfg)
		require.NoError(t, err)
		assert.Equal(t, time.Hour, access)
		assert.Equal(t, 168*time.Hour, refresh)
		assert.Equal(t, 10*time.Minute, authCode)
	})

	t.Run("invalid access token lifespan returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.TokenLifespanRunConfig{
			AccessTokenLifespan: "invalid",
		}

		_, _, _, err := parseTokenLifespans(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid access token lifespan")
	})

	t.Run("invalid refresh token lifespan returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.TokenLifespanRunConfig{
			RefreshTokenLifespan: "not-a-duration",
		}

		_, _, _, err := parseTokenLifespans(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid refresh token lifespan")
	})

	t.Run("invalid auth code lifespan returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.TokenLifespanRunConfig{
			AuthCodeLifespan: "bad",
		}

		_, _, _, err := parseTokenLifespans(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid auth code lifespan")
	})
}

func TestResolveSecret(t *testing.T) {
	t.Parallel()

	t.Run("returns empty string and no error when neither set", func(t *testing.T) {
		t.Parallel()

		result, err := resolveSecret("", "")
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("trims whitespace from file content", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "secret")

		require.NoError(t, os.WriteFile(secretFile, []byte("  secret-value  \n"), 0600))

		result, err := resolveSecret(secretFile, "")
		require.NoError(t, err)
		assert.Equal(t, "secret-value", result)
	})

	t.Run("returns error when file is set but unreadable", func(t *testing.T) {
		t.Parallel()

		result, err := resolveSecret("/nonexistent/file", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read secret file")
		assert.Empty(t, result)
	})

	t.Run("returns error when env var is specified but not populated", func(t *testing.T) {
		t.Parallel()

		// Use a unique env var name that won't be set in the environment
		envVar := "TEST_SECRET_NOT_SET_12345"

		result, err := resolveSecret("", envVar)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "environment variable")
		assert.Contains(t, err.Error(), "is not set")
		assert.Empty(t, result)
	})
}

// TestResolveSecretWithEnvVar tests resolveSecret with environment variables.
// These tests cannot use t.Parallel() because they use t.Setenv().
func TestResolveSecretWithEnvVar(t *testing.T) {
	t.Run("file takes precedence over env var", func(t *testing.T) {
		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "secret")
		fileSecret := "secret-from-file"

		require.NoError(t, os.WriteFile(secretFile, []byte(fileSecret), 0600))

		// Set an env var
		envVar := "TEST_SECRET_FILE_PRECEDENCE"
		t.Setenv(envVar, "secret-from-env")

		result, err := resolveSecret(secretFile, envVar)
		require.NoError(t, err)
		assert.Equal(t, fileSecret, result)
	})

	t.Run("reads from env var when only env var is set", func(t *testing.T) {
		envVar := "TEST_SECRET_ENV_ONLY"
		envSecret := "secret-from-env"
		t.Setenv(envVar, envSecret)

		result, err := resolveSecret("", envVar)
		require.NoError(t, err)
		assert.Equal(t, envSecret, result)
	})

	t.Run("returns error when file is set but missing (does not fall back to env)", func(t *testing.T) {
		envVar := "TEST_SECRET_NO_FALLBACK"
		t.Setenv(envVar, "secret-from-env")

		result, err := resolveSecret("/nonexistent/file", envVar)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read secret file")
		assert.Empty(t, result)
	})
}

func TestConvertUserInfoConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns nil", func(t *testing.T) {
		t.Parallel()

		result := convertUserInfoConfig(nil)
		assert.Nil(t, result)
	})

	t.Run("converts full config", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.UserInfoRunConfig{
			EndpointURL:       "https://example.com/userinfo",
			HTTPMethod:        "GET",
			AdditionalHeaders: map[string]string{"Accept": "application/json"},
			FieldMapping: &authserver.UserInfoFieldMappingRunConfig{
				SubjectFields: []string{"id", "sub"},
				NameFields:    []string{"name", "login"},
				EmailFields:   []string{"email"},
			},
		}

		result := convertUserInfoConfig(cfg)
		require.NotNil(t, result)

		assert.Equal(t, "https://example.com/userinfo", result.EndpointURL)
		assert.Equal(t, "GET", result.HTTPMethod)
		assert.Equal(t, map[string]string{"Accept": "application/json"}, result.AdditionalHeaders)

		require.NotNil(t, result.FieldMapping)
		assert.Equal(t, []string{"id", "sub"}, result.FieldMapping.SubjectFields)
		assert.Equal(t, []string{"name", "login"}, result.FieldMapping.NameFields)
		assert.Equal(t, []string{"email"}, result.FieldMapping.EmailFields)
	})

	t.Run("converts config without field mapping", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.UserInfoRunConfig{
			EndpointURL: "https://example.com/userinfo",
		}

		result := convertUserInfoConfig(cfg)
		require.NotNil(t, result)
		assert.Equal(t, "https://example.com/userinfo", result.EndpointURL)
		assert.Nil(t, result.FieldMapping)
	})
}

func TestConvertFieldMapping(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns nil", func(t *testing.T) {
		t.Parallel()

		result := convertFieldMapping(nil)
		assert.Nil(t, result)
	})

	t.Run("converts full config", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.UserInfoFieldMappingRunConfig{
			SubjectFields: []string{"id"},
			NameFields:    []string{"name"},
			EmailFields:   []string{"email"},
		}

		result := convertFieldMapping(cfg)
		require.NotNil(t, result)

		assert.Equal(t, []string{"id"}, result.SubjectFields)
		assert.Equal(t, []string{"name"}, result.NameFields)
		assert.Equal(t, []string{"email"}, result.EmailFields)
	})
}

func TestBuildPureOAuth2Config(t *testing.T) {
	t.Parallel()

	t.Run("nil OAuth2Config returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type:         authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: nil,
		}

		_, err := buildPureOAuth2Config(rc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oauth2_config required")
	})

	t.Run("builds valid config", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "client-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte("my-client-secret"), 0600))

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				ClientSecretFile:      secretFile,
				RedirectURI:           "https://my-app.com/callback",
				Scopes:                []string{"read", "write"},
				UserInfo: &authserver.UserInfoRunConfig{
					EndpointURL: "https://example.com/userinfo",
				},
			},
		}

		cfg, err := buildPureOAuth2Config(rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "https://example.com/authorize", cfg.AuthorizationEndpoint)
		assert.Equal(t, "https://example.com/token", cfg.TokenEndpoint)
		assert.Equal(t, "my-client-id", cfg.ClientID)
		assert.Equal(t, "my-client-secret", cfg.ClientSecret)
		assert.Equal(t, "https://my-app.com/callback", cfg.RedirectURI)
		assert.Equal(t, []string{"read", "write"}, cfg.Scopes)
		require.NotNil(t, cfg.UserInfo)
		assert.Equal(t, "https://example.com/userinfo", cfg.UserInfo.EndpointURL)
	})
}

// TestBuildPureOAuth2ConfigWithEnvVar tests buildPureOAuth2Config with environment variables.
// This test cannot use t.Parallel() because it uses t.Setenv().
func TestBuildPureOAuth2ConfigWithEnvVar(t *testing.T) {
	t.Run("resolves secret from env var when file missing", func(t *testing.T) {
		envVar := "TEST_CLIENT_SECRET_ENV"
		t.Setenv(envVar, "env-client-secret")

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				ClientSecretEnvVar:    envVar,
				RedirectURI:           "https://my-app.com/callback",
			},
		}

		cfg, err := buildPureOAuth2Config(rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "env-client-secret", cfg.ClientSecret)
	})
}

func TestNewHMACSecrets(t *testing.T) {
	t.Parallel()

	t.Run("creates secrets with current only", func(t *testing.T) {
		t.Parallel()

		current := []byte("my-current-secret-32-bytes-long!")
		secrets := servercrypto.NewHMACSecrets(current)

		require.NotNil(t, secrets)
		assert.Equal(t, current, secrets.Current)
		assert.Nil(t, secrets.Rotated)
	})
}

func TestNewEmbeddedAuthServer(t *testing.T) {
	t.Parallel()

	// createMinimalValidConfig creates a minimal valid RunConfig for testing.
	// It uses development mode defaults (no signing keys, no HMAC secrets) and
	// a pure OAuth2 upstream to avoid OIDC discovery.
	createMinimalValidConfig := func() *authserver.RunConfig {
		return &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "http://localhost:8080",
			// SigningKeyConfig nil = development mode (ephemeral key)
			// HMACSecretFiles empty = development mode (ephemeral secret)
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client-id",
						RedirectURI:           "http://localhost:8080/oauth/callback",
						// ClientSecret optional for public clients with PKCE
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	t.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()

		server, err := NewEmbeddedAuthServer(context.Background(), nil)
		require.Error(t, err)
		assert.Nil(t, server)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("valid config creates server with non-nil handler", func(t *testing.T) {
		t.Parallel()

		cfg := createMinimalValidConfig()

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, server)

		// Handler() should return non-nil
		handler := server.Handler()
		assert.NotNil(t, handler)

		// Clean up
		require.NoError(t, server.Close())
	})

	t.Run("Close succeeds", func(t *testing.T) {
		t.Parallel()

		cfg := createMinimalValidConfig()

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, server)

		// Close should succeed
		err = server.Close()
		require.NoError(t, err)

		// Note: Close() is currently not idempotent - the underlying MemoryStorage.Close()
		// panics on second call due to closing a closed channel. If idempotent Close()
		// is needed, the storage layer should be updated to handle this gracefully.
	})

	t.Run("invalid issuer URL returns error", func(t *testing.T) {
		t.Parallel()

		cfg := createMinimalValidConfig()
		cfg.Issuer = "not-a-valid-url"

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.Error(t, err)
		assert.Nil(t, server)
	})

	t.Run("missing upstreams returns error", func(t *testing.T) {
		t.Parallel()

		cfg := createMinimalValidConfig()
		cfg.Upstreams = nil

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.Error(t, err)
		assert.Nil(t, server)
	})

	t.Run("missing allowed audiences returns error", func(t *testing.T) {
		t.Parallel()

		cfg := createMinimalValidConfig()
		cfg.AllowedAudiences = nil

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.Error(t, err)
		assert.Nil(t, server)
	})
}

func TestBuildOIDCConfig(t *testing.T) {
	t.Parallel()

	// Constants for OIDC well-known paths used in test mocks
	const (
		wellKnownOIDCPath  = "/.well-known/openid-configuration"
		wellKnownOAuthPath = "/.well-known/oauth-authorization-server"
		httpScheme         = "http"
	)

	t.Run("nil OIDCConfig returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type:       authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: nil,
		}

		_, err := buildOIDCConfig(context.Background(), rc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oidc_config required")
	})

	t.Run("successful discovery populates endpoints", func(t *testing.T) {
		t.Parallel()

		// Create mock OIDC server with userinfo endpoint
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wellKnownOIDCPath ||
				r.URL.Path == wellKnownOAuthPath {
				issuerURL := httpScheme + "://" + r.Host

				doc := map[string]interface{}{
					"issuer":                 issuerURL,
					"authorization_endpoint": issuerURL + "/authorize",
					"token_endpoint":         issuerURL + "/token",
					"jwks_uri":               issuerURL + "/.well-known/jwks.json",
					"userinfo_endpoint":      issuerURL + "/userinfo",
				}

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(doc)
				require.NoError(t, err)
				return
			}
			http.NotFound(w, r)
		}))
		defer mockServer.Close()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   mockServer.URL,
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				Scopes:      []string{"openid", "profile"},
			},
		}

		cfg, err := buildOIDCConfig(context.Background(), rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Verify endpoints from discovery
		assert.Equal(t, mockServer.URL+"/authorize", cfg.AuthorizationEndpoint)
		assert.Equal(t, mockServer.URL+"/token", cfg.TokenEndpoint)

		// Verify client config is passed through
		assert.Equal(t, "test-client-id", cfg.ClientID)
		assert.Equal(t, "http://localhost:8080/callback", cfg.RedirectURI)
		assert.Equal(t, []string{"openid", "profile"}, cfg.Scopes)

		// Verify userinfo endpoint is populated from discovery
		require.NotNil(t, cfg.UserInfo)
		assert.Equal(t, mockServer.URL+"/userinfo", cfg.UserInfo.EndpointURL)
	})

	t.Run("UserInfoOverride takes precedence over discovered endpoint", func(t *testing.T) {
		t.Parallel()

		// Create mock OIDC server with a userinfo endpoint
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wellKnownOIDCPath ||
				r.URL.Path == wellKnownOAuthPath {
				issuerURL := httpScheme + "://" + r.Host

				doc := map[string]interface{}{
					"issuer":                 issuerURL,
					"authorization_endpoint": issuerURL + "/authorize",
					"token_endpoint":         issuerURL + "/token",
					"jwks_uri":               issuerURL + "/.well-known/jwks.json",
					"userinfo_endpoint":      issuerURL + "/discovered-userinfo",
				}

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(doc)
				require.NoError(t, err)
				return
			}
			http.NotFound(w, r)
		}))
		defer mockServer.Close()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   mockServer.URL,
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				UserInfoOverride: &authserver.UserInfoRunConfig{
					EndpointURL: "https://custom.example.com/userinfo",
					HTTPMethod:  "POST",
				},
			},
		}

		cfg, err := buildOIDCConfig(context.Background(), rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// UserInfoOverride should take precedence over discovered endpoint
		require.NotNil(t, cfg.UserInfo)
		assert.Equal(t, "https://custom.example.com/userinfo", cfg.UserInfo.EndpointURL)
		assert.Equal(t, "POST", cfg.UserInfo.HTTPMethod)
	})

	t.Run("no userinfo when not discovered and no override", func(t *testing.T) {
		t.Parallel()

		// Create mock OIDC server without userinfo endpoint
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wellKnownOIDCPath ||
				r.URL.Path == wellKnownOAuthPath {
				issuerURL := httpScheme + "://" + r.Host

				doc := map[string]interface{}{
					"issuer":                 issuerURL,
					"authorization_endpoint": issuerURL + "/authorize",
					"token_endpoint":         issuerURL + "/token",
					"jwks_uri":               issuerURL + "/.well-known/jwks.json",
					// No userinfo_endpoint
				}

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(doc)
				require.NoError(t, err)
				return
			}
			http.NotFound(w, r)
		}))
		defer mockServer.Close()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   mockServer.URL,
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				// No UserInfoOverride
			},
		}

		cfg, err := buildOIDCConfig(context.Background(), rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// UserInfo should be nil when not discovered and no override
		assert.Nil(t, cfg.UserInfo)
	})

	t.Run("discovery failure returns error", func(t *testing.T) {
		t.Parallel()

		// Create mock server that returns 404
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer mockServer.Close()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   mockServer.URL,
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
			},
		}

		_, err := buildOIDCConfig(context.Background(), rc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OIDC discovery failed")
	})

	t.Run("resolves client secret from file", func(t *testing.T) {
		t.Parallel()

		// Create mock OIDC server
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wellKnownOIDCPath ||
				r.URL.Path == wellKnownOAuthPath {
				issuerURL := httpScheme + "://" + r.Host

				doc := map[string]interface{}{
					"issuer":                 issuerURL,
					"authorization_endpoint": issuerURL + "/authorize",
					"token_endpoint":         issuerURL + "/token",
					"jwks_uri":               issuerURL + "/.well-known/jwks.json",
				}

				w.Header().Set("Content-Type", "application/json")
				err := json.NewEncoder(w).Encode(doc)
				require.NoError(t, err)
				return
			}
			http.NotFound(w, r)
		}))
		defer mockServer.Close()

		// Create secret file
		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "client-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte("my-oidc-client-secret"), 0600))

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:        mockServer.URL,
				ClientID:         "test-client-id",
				ClientSecretFile: secretFile,
				RedirectURI:      "http://localhost:8080/callback",
			},
		}

		cfg, err := buildOIDCConfig(context.Background(), rc)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "my-oidc-client-secret", cfg.ClientSecret)
	})
}
