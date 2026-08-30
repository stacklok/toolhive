// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	t.Run("preserves leading and trailing whitespace bytes in secrets", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "hmac-secret")
		secretValue := "  secret-with-whitespace-that-is-at-least-32-bytes  \n"

		err := os.WriteFile(secretFile, []byte(secretValue), 0600)
		require.NoError(t, err)

		secrets, err := loadHMACSecrets([]string{secretFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		assert.Equal(t, []byte(secretValue), secrets.Current)
	})

	t.Run("does not corrupt binary secrets with whitespace-valued edge bytes", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "hmac-secret")

		original := make([]byte, 32)
		for i := range original {
			original[i] = 0xAB
		}
		original[0] = 0x20
		original[31] = 0x0A

		require.NoError(t, os.WriteFile(secretFile, original, 0600))

		secrets, err := loadHMACSecrets([]string{secretFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		require.Len(t, secrets.Current, 32)
		assert.Equal(t, original, secrets.Current)
	})

	t.Run("does not corrupt rotated binary secrets with whitespace-valued edge bytes", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		currentFile := filepath.Join(tmpDir, "hmac-current")
		rotatedFile := filepath.Join(tmpDir, "hmac-rotated")

		require.NoError(t, os.WriteFile(currentFile, []byte("current-secret-32-bytes-minimum!"), 0600))

		original := make([]byte, 32)
		for i := range original {
			original[i] = 0xAB
		}
		original[0] = 0x20
		original[31] = 0x0A

		require.NoError(t, os.WriteFile(rotatedFile, original, 0600))

		secrets, err := loadHMACSecrets([]string{currentFile, rotatedFile})
		require.NoError(t, err)
		require.NotNil(t, secrets)

		require.Len(t, secrets.Rotated, 1)
		assert.Equal(t, original, secrets.Rotated[0])
	})

	t.Run("short current secret returns error naming file and byte counts", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "hmac-secret")

		require.NoError(t, os.WriteFile(secretFile, make([]byte, 31), 0600))

		_, err := loadHMACSecrets([]string{secretFile})
		require.Error(t, err)
		assert.Contains(t, err.Error(), secretFile)
		assert.Contains(t, err.Error(), "31")
		assert.Contains(t, err.Error(), "32")
	})

	t.Run("short rotated secret returns error naming file and byte counts", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		currentFile := filepath.Join(tmpDir, "hmac-current")
		rotatedFile := filepath.Join(tmpDir, "hmac-rotated")

		require.NoError(t, os.WriteFile(currentFile, []byte("current-secret-32-bytes-minimum!"), 0600))
		require.NoError(t, os.WriteFile(rotatedFile, make([]byte, 31), 0600))

		_, err := loadHMACSecrets([]string{currentFile, rotatedFile})
		require.Error(t, err)
		assert.Contains(t, err.Error(), rotatedFile)
		assert.Contains(t, err.Error(), "31")
		assert.Contains(t, err.Error(), "32")
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

// TestResolveSecret pins the observable contract of the runner-package
// resolveSecret helper: file-precedence, whitespace-trimming, and the
// explicit error modes for missing-file / unset-env. resolveSecret is
// the single authoritative implementation in the codebase; the
// pkg/auth/dcr package no longer carries a parallel copy (removed in
// #5219 sub-issue 4b, when the resolver's input was neutralised and the
// embedded-authserver adapter took responsibility for resolving the
// file-or-env reference into Request.InitialAccessToken at the call
// site).
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

		_, err := buildPureOAuth2Config(rc, false)
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

		cfg, err := buildPureOAuth2Config(rc, false)
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

	t.Run("propagates AdditionalAuthorizationParams", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				RedirectURI:           "https://my-app.com/callback",
				AdditionalAuthorizationParams: map[string]string{
					"access_type": "offline",
				},
			},
		}

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, map[string]string{"access_type": "offline"},
			cfg.AdditionalAuthorizationParams)
	})

	t.Run("rejects config with neither ClientID nor DCRConfig", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				RedirectURI:           "https://my-app.com/callback",
			},
		}

		_, err := buildPureOAuth2Config(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "client_id or dcr_config is required")
	})

	t.Run("rejects config with both ClientID and DCRConfig", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				RedirectURI:           "https://my-app.com/callback",
				DCRConfig: &authserver.DCRUpstreamConfig{
					RegistrationEndpoint: "https://example.com/register",
				},
			},
		}

		_, err := buildPureOAuth2Config(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("accepts DCRConfig without ClientID", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				RedirectURI:           "https://my-app.com/callback",
				DCRConfig: &authserver.DCRUpstreamConfig{
					RegistrationEndpoint: "https://example.com/register",
				},
			},
		}

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Empty(t, cfg.ClientID)
	})

	t.Run("propagates AllowPrivateIPs", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				RedirectURI:           "https://my-app.com/callback",
				AllowPrivateIPs:       true,
			},
		}

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.True(t, cfg.AllowPrivateIPs)
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

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "env-client-secret", cfg.ClientSecret)
	})
}

func TestBuildPureOAuth2ConfigIdentityFromToken(t *testing.T) {
	t.Parallel()

	baseRC := func() *authserver.UpstreamRunConfig {
		return &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				ClientID:              "my-client-id",
				RedirectURI:           "https://my-app.com/callback",
			},
		}
	}

	t.Run("nil IdentityFromToken produces nil in runtime config", func(t *testing.T) {
		t.Parallel()

		rc := baseRC()
		// IdentityFromToken is not set

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Nil(t, cfg.IdentityFromToken, "IdentityFromToken must be nil when not configured")
	})

	t.Run("all three paths round-trip correctly", func(t *testing.T) {
		t.Parallel()

		rc := baseRC()
		rc.OAuth2Config.IdentityFromToken = &authserver.IdentityFromTokenRunConfig{
			SubjectPath: "username",
			NamePath:    "display_name",
			EmailPath:   "email",
		}

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		require.NotNil(t, cfg.IdentityFromToken)
		assert.Equal(t, "username", cfg.IdentityFromToken.SubjectPath)
		assert.Equal(t, "display_name", cfg.IdentityFromToken.NamePath)
		assert.Equal(t, "email", cfg.IdentityFromToken.EmailPath)
	})

	t.Run("only SubjectPath set, name and email empty", func(t *testing.T) {
		t.Parallel()

		rc := baseRC()
		rc.OAuth2Config.IdentityFromToken = &authserver.IdentityFromTokenRunConfig{
			SubjectPath: "authed_user.id",
		}

		cfg, err := buildPureOAuth2Config(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		require.NotNil(t, cfg.IdentityFromToken)
		assert.Equal(t, "authed_user.id", cfg.IdentityFromToken.SubjectPath)
		assert.Empty(t, cfg.IdentityFromToken.NamePath)
		assert.Empty(t, cfg.IdentityFromToken.EmailPath)
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

		// Close is idempotent - calling it again should not panic and should return
		// the same error (nil in this case)
		err = server.Close()
		require.NoError(t, err)
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

func TestEmbeddedAuthServer_KeyProvider(t *testing.T) {
	t.Parallel()

	t.Run("returns non-nil KeyProvider after construction", func(t *testing.T) {
		t.Parallel()

		cfg := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "http://localhost:8080",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client-id",
						RedirectURI:           "http://localhost:8080/oauth/callback",
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}

		server, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, server)
		defer func() { _ = server.Close() }()

		provider := server.KeyProvider()
		require.NotNil(t, provider, "KeyProvider should be non-nil after construction")

		// Verify it can return public keys
		pubKeys, err := provider.PublicKeys(context.Background())
		require.NoError(t, err)
		assert.NotEmpty(t, pubKeys, "KeyProvider should have at least one public key")
	})
}

func TestBuildUpstreamConfig(t *testing.T) {
	t.Parallel()

	t.Run("OIDC type returns UpstreamConfig with OIDCConfig", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Name: "google",
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   "https://accounts.google.com",
				ClientID:    "my-client-id",
				RedirectURI: "http://localhost:8080/callback",
				Scopes:      []string{"openid", "email"},
			},
		}

		cfg, err := buildUpstreamConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "google", cfg.Name)
		assert.Equal(t, authserver.UpstreamProviderTypeOIDC, cfg.Type)
		require.NotNil(t, cfg.OIDCConfig, "OIDCConfig should be set for OIDC type")
		assert.Nil(t, cfg.OAuth2Config, "OAuth2Config should be nil for OIDC type")
		assert.Equal(t, "https://accounts.google.com", cfg.OIDCConfig.Issuer)
		assert.Equal(t, "my-client-id", cfg.OIDCConfig.ClientID)
		assert.Equal(t, []string{"openid", "email"}, cfg.OIDCConfig.Scopes)
	})

	t.Run("OAuth2 type returns UpstreamConfig with OAuth2Config", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Name: "github",
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
				TokenEndpoint:         "https://github.com/login/oauth/access_token",
				ClientID:              "gh-client-id",
				RedirectURI:           "http://localhost:8080/callback",
			},
		}

		cfg, err := buildUpstreamConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "github", cfg.Name)
		assert.Equal(t, authserver.UpstreamProviderTypeOAuth2, cfg.Type)
		require.NotNil(t, cfg.OAuth2Config, "OAuth2Config should be set for OAuth2 type")
		assert.Nil(t, cfg.OIDCConfig, "OIDCConfig should be nil for OAuth2 type")
		assert.Equal(t, "gh-client-id", cfg.OAuth2Config.ClientID)
		assert.Equal(t, "https://github.com/login/oauth/authorize", cfg.OAuth2Config.AuthorizationEndpoint)
	})

	t.Run("unknown type returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Name: "unknown-provider",
			Type: authserver.UpstreamProviderType("saml"),
		}

		_, err := buildUpstreamConfig(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported upstream type")
		assert.Contains(t, err.Error(), "saml")
	})

	t.Run("OIDC type with nil OIDCConfig returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Name:       "broken",
			Type:       authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: nil,
		}

		_, err := buildUpstreamConfig(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oidc_config required")
	})

	t.Run("OAuth2 type with nil OAuth2Config returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Name:         "broken",
			Type:         authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: nil,
		}

		_, err := buildUpstreamConfig(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oauth2_config required")
	})
}

func TestBuildOIDCConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil OIDCConfig returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type:       authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: nil,
		}

		_, err := buildOIDCConfig(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "oidc_config required")
	})

	t.Run("builds config with issuer and client credentials", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   "https://example.com",
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				Scopes:      []string{"openid", "profile"},
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Verify issuer is set (discovery happens in factory)
		assert.Equal(t, "https://example.com", cfg.Issuer)

		// Verify client config is passed through
		assert.Equal(t, "test-client-id", cfg.ClientID)
		assert.Equal(t, "http://localhost:8080/callback", cfg.RedirectURI)
		assert.Equal(t, []string{"openid", "profile"}, cfg.Scopes)
	})

	t.Run("applies default scopes when not specified", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   "https://example.com",
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				// No scopes specified
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// Verify default scopes are applied
		assert.Equal(t, []string{"openid", "offline_access"}, cfg.Scopes)
	})

	t.Run("resolves client secret from file", func(t *testing.T) {
		t.Parallel()

		// Create secret file
		tmpDir := t.TempDir()
		secretFile := filepath.Join(tmpDir, "client-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte("my-oidc-client-secret"), 0600))

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:        "https://example.com",
				ClientID:         "test-client-id",
				ClientSecretFile: secretFile,
				RedirectURI:      "http://localhost:8080/callback",
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "my-oidc-client-secret", cfg.ClientSecret)
	})

	t.Run("missing secret file returns error", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:        "https://example.com",
				ClientID:         "test-client-id",
				ClientSecretFile: "/nonexistent/secret",
				RedirectURI:      "http://localhost:8080/callback",
			},
		}

		_, err := buildOIDCConfig(rc, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve OIDC client secret")
	})

	t.Run("UserInfoOverride is ignored without error", func(t *testing.T) {
		t.Parallel()

		// UserInfoOverride is intentionally not propagated to upstream.OIDCConfig
		// because OIDC providers resolve identity from ID tokens, not UserInfo.
		// This test documents that behavior.
		rc := &authserver.UpstreamRunConfig{
			Name: "with-userinfo-override",
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   "https://example.com",
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				UserInfoOverride: &authserver.UserInfoRunConfig{
					EndpointURL: "https://example.com/userinfo",
				},
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		// OIDCConfig has no UserInfo field - verify the config is otherwise valid
		assert.Equal(t, "https://example.com", cfg.Issuer)
		assert.Equal(t, "test-client-id", cfg.ClientID)
	})

	t.Run("propagates AdditionalAuthorizationParams", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:   "https://example.com",
				ClientID:    "test-client-id",
				RedirectURI: "http://localhost:8080/callback",
				AdditionalAuthorizationParams: map[string]string{
					"access_type": "offline",
				},
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, map[string]string{"access_type": "offline"},
			cfg.AdditionalAuthorizationParams)
	})

	t.Run("propagates SubjectClaim", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:    "https://example.com",
				ClientID:     "test-client-id",
				RedirectURI:  "http://localhost:8080/callback",
				SubjectClaim: "oid",
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.Equal(t, "oid", cfg.SubjectClaim)
	})

	t.Run("propagates AllowPrivateIPs", func(t *testing.T) {
		t.Parallel()

		rc := &authserver.UpstreamRunConfig{
			Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL:       "https://idp.example.com",
				ClientID:        "my-client-id",
				RedirectURI:     "http://localhost:8080/callback",
				AllowPrivateIPs: true,
			},
		}

		cfg, err := buildOIDCConfig(rc, false)
		require.NoError(t, err)
		require.NotNil(t, cfg)

		assert.True(t, cfg.AllowPrivateIPs)
	})
}

func TestCreateStorage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("nil config returns memory storage", func(t *testing.T) {
		t.Parallel()

		stor, err := createStorage(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, stor)
		_, ok := stor.(*storage.MemoryStorage)
		assert.True(t, ok, "expected MemoryStorage")
	})

	t.Run("empty type returns memory storage", func(t *testing.T) {
		t.Parallel()

		stor, err := createStorage(ctx, &storage.RunConfig{})
		require.NoError(t, err)
		require.NotNil(t, stor)
		_, ok := stor.(*storage.MemoryStorage)
		assert.True(t, ok, "expected MemoryStorage")
	})

	t.Run("explicit memory type returns memory storage", func(t *testing.T) {
		t.Parallel()

		stor, err := createStorage(ctx, &storage.RunConfig{
			Type: string(storage.TypeMemory),
		})
		require.NoError(t, err)
		require.NotNil(t, stor)
		_, ok := stor.(*storage.MemoryStorage)
		assert.True(t, ok, "expected MemoryStorage")
	})

	t.Run("unsupported type returns error", func(t *testing.T) {
		t.Parallel()

		_, err := createStorage(ctx, &storage.RunConfig{
			Type: "dynamodb",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported storage type")
	})

	t.Run("redis type with nil RedisConfig returns error", func(t *testing.T) {
		t.Parallel()

		_, err := createStorage(ctx, &storage.RunConfig{
			Type: string(storage.TypeRedis),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redis config is required")
	})

}

// TestConvertRedisRunConfig covers the runner-owned conversion steps: nil
// guard and ACL credential resolution. Connection-mode topology validation is
// owned by the shared toolhive-core redis package and exercised in its tests.
func TestConvertRedisRunConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		_, err := convertRedisRunConfig(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redis config is required")
	})

	t.Run("missing ACL user config returns error", func(t *testing.T) {
		t.Parallel()
		_, err := convertRedisRunConfig(&storage.RedisRunConfig{
			KeyPrefix: "test:",
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"localhost:26379"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "acl user config is required")
	})

	t.Run("unset username env var returns error", func(t *testing.T) {
		t.Parallel()
		_, err := convertRedisRunConfig(&storage.RedisRunConfig{
			KeyPrefix: "test:",
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"localhost:26379"},
			},
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "NONEXISTENT_REDIS_USER_VAR_12345",
				PasswordEnvVar: "NONEXISTENT_REDIS_PASS_VAR_12345",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve Redis username")
	})
}

// TestConvertRedisRunConfig_WithEnvVars tests convertRedisRunConfig with environment variables.
// These subtests use t.Setenv which is incompatible with t.Parallel.
func TestConvertRedisRunConfig_WithEnvVars(t *testing.T) {
	t.Run("valid config with env vars resolves correctly", func(t *testing.T) {
		t.Setenv("TEST_REDIS_USER_CONV", "myuser")
		t.Setenv("TEST_REDIS_PASS_CONV", "mypass")

		cfg, err := convertRedisRunConfig(&storage.RedisRunConfig{
			KeyPrefix: "thv:auth:ns:name:",
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"10.0.0.1:26379", "10.0.0.2:26379"},
				DB:            3,
			},
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "TEST_REDIS_USER_CONV",
				PasswordEnvVar: "TEST_REDIS_PASS_CONV",
			},
			DialTimeout:  "10s",
			ReadTimeout:  "5s",
			WriteTimeout: "3s",
		})
		require.NoError(t, err)

		require.NotNil(t, cfg.SentinelConfig)
		assert.Equal(t, "mymaster", cfg.SentinelConfig.MasterName)
		assert.Equal(t, []string{"10.0.0.1:26379", "10.0.0.2:26379"}, cfg.SentinelConfig.SentinelAddrs)
		assert.Equal(t, 3, cfg.DB)
		assert.Equal(t, "myuser", cfg.Username)
		assert.Equal(t, "mypass", cfg.Password)
		assert.Equal(t, 10*time.Second, cfg.DialTimeout)
		assert.Equal(t, 5*time.Second, cfg.ReadTimeout)
		assert.Equal(t, 3*time.Second, cfg.WriteTimeout)
	})

	t.Run("invalid timeout duration returns error", func(t *testing.T) {
		t.Setenv("TEST_REDIS_USER_TO", "myuser")
		t.Setenv("TEST_REDIS_PASS_TO", "mypass")

		_, err := convertRedisRunConfig(&storage.RedisRunConfig{
			KeyPrefix: "test:",
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"localhost:26379"},
			},
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "TEST_REDIS_USER_TO",
				PasswordEnvVar: "TEST_REDIS_PASS_TO",
			},
			DialTimeout: "not-a-duration",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid dial timeout")
	})

	t.Run("zero timeouts use defaults from RedisConfig", func(t *testing.T) {
		t.Setenv("TEST_REDIS_USER_ZT", "myuser")
		t.Setenv("TEST_REDIS_PASS_ZT", "mypass")

		cfg, err := convertRedisRunConfig(&storage.RedisRunConfig{
			KeyPrefix: "test:",
			SentinelConfig: &storage.SentinelRunConfig{
				MasterName:    "mymaster",
				SentinelAddrs: []string{"localhost:26379"},
			},
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "TEST_REDIS_USER_ZT",
				PasswordEnvVar: "TEST_REDIS_PASS_ZT",
			},
			// No timeouts set — should remain zero, defaults applied by NewRedisStorage
		})
		require.NoError(t, err)
		assert.Zero(t, cfg.DialTimeout)
		assert.Zero(t, cfg.ReadTimeout)
		assert.Zero(t, cfg.WriteTimeout)
	})

	t.Run("standalone addr, no sentinel config", func(t *testing.T) {
		t.Setenv("TOOLHIVE_AUTH_SERVER_REDIS_USERNAME", "user")
		t.Setenv("TOOLHIVE_AUTH_SERVER_REDIS_PASSWORD", "pass")
		cfg, err := convertRedisRunConfig(&storage.RedisRunConfig{
			Addr: "redis.example.com:6379",
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "TOOLHIVE_AUTH_SERVER_REDIS_USERNAME",
				PasswordEnvVar: "TOOLHIVE_AUTH_SERVER_REDIS_PASSWORD",
			},
			KeyPrefix: "thv:auth:ns:name:",
		})
		require.NoError(t, err)
		assert.Equal(t, "redis.example.com:6379", cfg.Addr)
		assert.Nil(t, cfg.SentinelConfig)
	})

	t.Run("empty UsernameEnvVar uses legacy password-only auth", func(t *testing.T) {
		t.Setenv("TEST_REDIS_PASS_LEGACY", "mypass")

		cfg, err := convertRedisRunConfig(&storage.RedisRunConfig{
			Addr:      "memorystore.example.com:6379",
			KeyPrefix: "thv:auth:ns:name:",
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "", // omitted: triggers legacy AUTH <password>
				PasswordEnvVar: "TEST_REDIS_PASS_LEGACY",
			},
		})
		require.NoError(t, err)
		assert.Empty(t, cfg.Username)
		assert.Equal(t, "mypass", cfg.Password)
	})

	t.Run("cluster mode resolves correctly", func(t *testing.T) {
		t.Setenv("TEST_REDIS_USER_CLUSTER", "clusteruser")
		t.Setenv("TEST_REDIS_PASS_CLUSTER", "clusterpass")

		cfg, err := convertRedisRunConfig(&storage.RedisRunConfig{
			Addr:        "discovery.example.com:6379",
			ClusterMode: true,
			ACLUserConfig: &storage.ACLUserRunConfig{
				UsernameEnvVar: "TEST_REDIS_USER_CLUSTER",
				PasswordEnvVar: "TEST_REDIS_PASS_CLUSTER",
			},
			KeyPrefix: "thv:auth:ns:name:",
		})
		require.NoError(t, err)
		assert.Equal(t, "discovery.example.com:6379", cfg.Addr)
		assert.True(t, cfg.ClusterMode)
		assert.Nil(t, cfg.SentinelConfig)
		assert.Equal(t, "clusteruser", cfg.Username)
		assert.Equal(t, "clusterpass", cfg.Password)
	})
}

// stubServer is a minimal authserver.Server implementation for testing
// RegisterHandlers. It returns a fixed http.Handler that writes a 200 response
// with a marker body, and no-ops on all other interface methods.
//
// CloseIdleConnections is not part of authserver.Server — it is the optional
// capability authserver.CloseIdleConnections detects — so this stub implements
// it only to assert that the wrapper forwards. capabilityFreeStubServer covers a
// server that does not. Note it must not be embedded to build that one: the
// method would be promoted and the capability satisfied after all.
type stubServer struct {
	handler    http.Handler
	idleCloses atomic.Int32
}

func (s *stubServer) Handler() http.Handler                                { return s.handler }
func (*stubServer) IDPTokenStorage() storage.UpstreamTokenStorage          { return nil }
func (*stubServer) UpstreamTokenRefresher() storage.UpstreamTokenRefresher { return nil }
func (*stubServer) DCRStore() storage.DCRCredentialStore                   { return nil }
func (s *stubServer) CloseIdleConnections()                                { s.idleCloses.Add(1) }
func (*stubServer) Close() error                                           { return nil }

// capabilityFreeStubServer omits CloseIdleConnections, as an out-of-tree
// authserver.Server implementation written before the capability existed would.
// It counts Close so a test can prove the wrapper does not fall back to it.
type capabilityFreeStubServer struct {
	closed atomic.Int32
}

func (*capabilityFreeStubServer) Handler() http.Handler                         { return nil }
func (*capabilityFreeStubServer) IDPTokenStorage() storage.UpstreamTokenStorage { return nil }
func (*capabilityFreeStubServer) DCRStore() storage.DCRCredentialStore          { return nil }

func (*capabilityFreeStubServer) UpstreamTokenRefresher() storage.UpstreamTokenRefresher {
	return nil
}

func (s *capabilityFreeStubServer) Close() error {
	s.closed.Add(1)
	return nil
}

func TestRoutes(t *testing.T) {
	t.Parallel()

	stub := &stubServer{
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	eas := &EmbeddedAuthServer{server: stub}

	routes := eas.Routes()

	expectedKeys := []string{
		"/.well-known/openid-configuration",
		"/.well-known/openid-configuration/",
		"/.well-known/oauth-authorization-server",
		"/.well-known/oauth-authorization-server/",
		"/.well-known/jwks.json",
		"/oauth/",
	}

	require.Len(t, routes, len(expectedKeys), "Routes() should return exactly %d entries", len(expectedKeys))
	for _, key := range expectedKeys {
		handler, ok := routes[key]
		assert.True(t, ok, "Routes() should contain key %q", key)
		assert.NotNil(t, handler, "handler for %q should not be nil", key)
	}
}

func TestRegisterHandlers(t *testing.T) {
	t.Parallel()

	// Build an EmbeddedAuthServer backed by a stub that echoes the request path.
	stub := &stubServer{
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "handled:%s", r.URL.Path)
		}),
	}
	eas := &EmbeddedAuthServer{server: stub}

	mux := http.NewServeMux()
	eas.RegisterHandlers(mux)

	registeredPaths := []string{
		"/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server",
		"/.well-known/jwks.json",
	}

	for _, path := range registeredPaths {
		t.Run("registered path "+path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code,
				"expected 200 for registered path %s", path)
			assert.Equal(t, "handled:"+path, rec.Body.String(),
				"expected handler to receive the original path")
		})
	}

	// /oauth/ is registered as a prefix — any subpath should be routed.
	oauthSubPaths := []string{
		"/oauth/authorize",
		"/oauth/token",
		"/oauth/callback",
		"/oauth/register",
	}

	for _, path := range oauthSubPaths {
		t.Run("oauth prefix path "+path, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code,
				"expected 200 for oauth subpath %s", path)
			assert.Equal(t, "handled:"+path, rec.Body.String(),
				"expected handler to receive the original path")
		})
	}

	t.Run("unregistered well-known path returns 404", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/unknown", nil)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code,
			"expected 404 for unregistered well-known path")
	})

	t.Run("unregistered root path returns 404", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/other", nil)
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code,
			"expected 404 for unregistered root path")
	})
}

// newMockAuthorizationServer stands up a mock authorization server that
// serves RFC 8414 discovery metadata and an RFC 7591 /register endpoint.
// Every request is counted via the returned *int32 so tests can assert that
// cache hits issue zero additional network I/O. The issuer advertised in
// metadata is the server's own URL (loopback), which satisfies the HTTPS
// redirect-URI policy in resolveUpstreamRedirectURI.
func newMockAuthorizationServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()

	var total int32
	var server *httptest.Server

	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&total, 1)
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL,
			AuthorizationEndpoint:             server.URL + "/authorize",
			TokenEndpoint:                     server.URL + "/token",
			RegistrationEndpoint:              server.URL + "/register",
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
			ScopesSupported:                   []string{"openid", "profile"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&total, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		var req oauthproto.DynamicClientRegistrationRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "dcr-client-id",
			ClientSecret:            "dcr-client-secret",
			RegistrationAccessToken: "dcr-reg-token",
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Catch-all to count unexpected requests.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&total, 1)
		w.WriteHeader(http.StatusNotFound)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &total
}

// TestBuildUpstreamConfigs_DCR verifies the end-to-end DCR wiring inside
// buildUpstreamConfigs: on first call it registers with the mock AS and
// overlays the resolved client_id/client_secret; on second call it hits the
// in-memory store and issues zero additional HTTP requests; and neither call
// mutates the caller's original RunConfig.Upstreams slice.
func TestBuildUpstreamConfigs_DCR(t *testing.T) {
	t.Parallel()

	t.Run("DCR boot registers and overlays credentials", func(t *testing.T) {
		t.Parallel()

		server, requestCount := newMockAuthorizationServer(t)

		cfg := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        server.URL,
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "dcr-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						// ClientID intentionally empty: triggers DCR.
						ClientID:              "",
						AuthorizationEndpoint: server.URL + "/authorize",
						TokenEndpoint:         server.URL + "/token",
						Scopes:                []string{"openid", "profile"},
						// The mock IdP is a loopback listener, and a registration endpoint read
						// out of its metadata is dialed under the strict server-supplied policy.
						DCRConfig: &authserver.DCRUpstreamConfig{
							DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
						},
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}

		store := newMemoryDCRStore(t)
		got, err := buildUpstreamConfigs(context.Background(), cfg.Upstreams, cfg.Issuer, store, false)
		require.NoError(t, err)
		require.Len(t, got, 1)

		// DCR-resolved credentials appear on the built OAuth2Config.
		require.NotNil(t, got[0].OAuth2Config)
		assert.Equal(t, "dcr-client-id", got[0].OAuth2Config.ClientID)
		assert.Equal(t, "dcr-client-secret", got[0].OAuth2Config.ClientSecret)

		// Store now contains the resolution under the canonical storage.DCRKey.
		redirectURI := server.URL + "/oauth/callback"
		key := storage.DCRKey{
			Issuer:      server.URL,
			UpstreamID:  server.URL,
			RedirectURI: redirectURI,
			ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
		}
		cached, ok, err := store.Get(context.Background(), key)
		require.NoError(t, err)
		require.True(t, ok, "store should contain the DCR resolution keyed by storage.DCRKey")
		assert.Equal(t, "dcr-client-id", cached.ClientID)

		// First call must have hit the mock AS at least once (metadata +
		// register). The exact count depends on well-known path fallbacks,
		// but it must be strictly greater than zero.
		assert.Greater(t, atomic.LoadInt32(requestCount), int32(0),
			"DCR boot should have issued network I/O to the mock AS")

		// Copy-before-mutate: the caller's original slice element is
		// unchanged — ClientID is still empty; DCRConfig is still set.
		assert.Equal(t, "", cfg.Upstreams[0].OAuth2Config.ClientID,
			"original OAuth2Config.ClientID must not be mutated by DCR resolution")
		assert.NotNil(t, cfg.Upstreams[0].OAuth2Config.DCRConfig,
			"original OAuth2Config.DCRConfig must not be cleared by DCR resolution")
	})

	t.Run("cache hit on second call issues zero additional HTTP requests", func(t *testing.T) {
		t.Parallel()

		server, requestCount := newMockAuthorizationServer(t)

		cfg := &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        server.URL,
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "dcr-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						ClientID:              "",
						AuthorizationEndpoint: server.URL + "/authorize",
						TokenEndpoint:         server.URL + "/token",
						Scopes:                []string{"openid", "profile"},
						// The mock IdP is a loopback listener, and a registration endpoint read
						// out of its metadata is dialed under the strict server-supplied policy.
						DCRConfig: &authserver.DCRUpstreamConfig{
							DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
						},
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}

		store := newMemoryDCRStore(t)

		// First call: populates the store.
		_, err := buildUpstreamConfigs(context.Background(), cfg.Upstreams, cfg.Issuer, store, false)
		require.NoError(t, err)
		firstCallRequests := atomic.LoadInt32(requestCount)
		require.Greater(t, firstCallRequests, int32(0),
			"first call should have issued network I/O")

		// Second call: must short-circuit on the cache and issue zero
		// additional HTTP requests against the mock AS.
		got, err := buildUpstreamConfigs(context.Background(), cfg.Upstreams, cfg.Issuer, store, false)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "dcr-client-id", got[0].OAuth2Config.ClientID)
		assert.Equal(t, "dcr-client-secret", got[0].OAuth2Config.ClientSecret)

		assert.Equal(t, firstCallRequests, atomic.LoadInt32(requestCount),
			"second call must not issue any additional HTTP requests to the mock AS")

		// Copy-before-mutate still holds after both calls.
		assert.Equal(t, "", cfg.Upstreams[0].OAuth2Config.ClientID,
			"original OAuth2Config.ClientID must remain empty after both calls")
		assert.NotNil(t, cfg.Upstreams[0].OAuth2Config.DCRConfig,
			"original OAuth2Config.DCRConfig must remain set after both calls")
	})
}

// TestNewEmbeddedAuthServer_DCRBoot drives the full NewEmbeddedAuthServer
// boot path against a mock upstream AS: signing keys are generated
// ephemerally, storage defaults to memory, and the DCR resolver runs
// inside the constructor. It verifies that (a) the constructor wires
// the shared storage.DCRCredentialStore into the DCR resolver (via the
// dcr.CredentialStore adapter passed to buildUpstreamConfigs), (b) that
// store is populated with the canonical storage.DCRKey after boot, and
// (c) the caller's original RunConfig.Upstreams[i] slice element is
// unchanged.
//
// This complements TestBuildUpstreamConfigs_DCR by exercising the full
// wiring — signing-key creation, HMAC secret defaults, storage
// instantiation, authserver.New() — rather than the internal helper in
// isolation.
func TestNewEmbeddedAuthServer_DCRBoot(t *testing.T) {
	t.Parallel()

	server, requestCount := newMockAuthorizationServer(t)

	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        server.URL,
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "dcr-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					ClientID:              "",
					AuthorizationEndpoint: server.URL + "/authorize",
					TokenEndpoint:         server.URL + "/token",
					Scopes:                []string{"openid", "profile"},
					// The mock IdP is a loopback listener, and a registration endpoint read
					// out of its metadata is dialed under the strict server-supplied policy.
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
					},
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	// Retain a pointer to the caller's OAuth2Config to verify that the
	// constructor did not mutate it via the shared pointer.
	originalOAuth2 := cfg.Upstreams[0].OAuth2Config

	embed, err := NewEmbeddedAuthServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, embed)
	t.Cleanup(func() { _ = embed.Close() })

	// The constructor must have wired a non-nil DCR store.
	dcrStore := embed.DCRStore()
	require.NotNil(t, dcrStore, "NewEmbeddedAuthServer must wire a DCR store")

	// The DCR registration must have hit the mock AS at least once.
	assert.Greater(t, atomic.LoadInt32(requestCount), int32(0),
		"DCR boot should have issued network I/O to the mock AS")

	// The store on the EmbeddedAuthServer contains the canonical
	// storage.DCRKey for this upstream — the accessor delegates to the
	// same storage.DCRCredentialStore createStorage produced, so a
	// successful boot persisted the resolution there directly (no
	// separate in-memory store was created).
	redirectURI := server.URL + "/oauth/callback"
	key := storage.DCRKey{
		Issuer:      server.URL,
		UpstreamID:  server.URL,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	cached, err := dcrStore.GetDCRCredentials(context.Background(), key)
	require.NoError(t, err, "DCR store on EmbeddedAuthServer must hold the DCR resolution")
	require.NotNil(t, cached)
	assert.Equal(t, "dcr-client-id", cached.ClientID)
	assert.Equal(t, "dcr-client-secret", cached.ClientSecret)

	// Copy-before-mutate: the caller's OAuth2Config pointer is unchanged.
	assert.Equal(t, "", originalOAuth2.ClientID,
		"NewEmbeddedAuthServer must not mutate the caller's OAuth2Config.ClientID")
	assert.NotNil(t, originalOAuth2.DCRConfig,
		"NewEmbeddedAuthServer must not clear the caller's OAuth2Config.DCRConfig")
	assert.Same(t, originalOAuth2, cfg.Upstreams[0].OAuth2Config,
		"NewEmbeddedAuthServer must not replace the caller's OAuth2Config pointer")
}

// closeTrackingStorage wraps an authserver storage and counts Close calls.
// It implements storage.Storage by embedding the wrapped value, then
// overriding Close to record the call before delegating to the inner
// Storage. Used by TestNewEmbeddedAuthServer_ClosesStorageOnError to
// verify the deferred-cleanup contract on NewEmbeddedAuthServer's error
// paths without depending on goroutine-count heuristics (which are
// confounded by HTTP transport keep-alive goroutines this package does
// not own).
type closeTrackingStorage struct {
	storage.Storage
	closeCount atomic.Int32
}

func (s *closeTrackingStorage) Close() error {
	s.closeCount.Add(1)
	return s.Storage.Close()
}

// TestNewEmbeddedAuthServer_ClosesStorageOnError pins the post-#5185
// invariant that NewEmbeddedAuthServer never leaks the storage backend on
// the constructor's error paths.
//
// Before the wiring change, createStorage ran late in the constructor so
// most error paths (DCR resolver, upstream config build) returned without
// having opened the backend. After the change, createStorage runs first —
// so a DCR failure (the most likely failure mode in production: upstream
// AS unreachable, /register 4xx) returns from the constructor with the
// storage still holding OS-level resources (Redis client connection pool,
// MemoryStorage cleanup goroutine). Without the deferred cleanup, a
// crash-looping pod would leak one connection pool / goroutine per
// restart.
//
// The test calls NewEmbeddedAuthServerWithStorage (the test seam that
// production NewEmbeddedAuthServer dispatches into) so the storage
// instance is observable: a closeTrackingStorage wrapper records every
// Close call. The assertion is then a direct count rather than a
// goroutine-count heuristic. This avoids the package-level swap-pattern
// that would otherwise force the test to run non-parallel.
func TestNewEmbeddedAuthServer_ClosesStorageOnError(t *testing.T) {
	t.Parallel()

	tracker := &closeTrackingStorage{Storage: storage.NewMemoryStorage()}

	// Always-500 discovery endpoint forces the DCR resolver to fail
	// during buildUpstreamConfigs — i.e. inside the leak window between
	// createStorage success and EmbeddedAuthServer construction.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        server.URL,
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "dcr-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					ClientID:              "",
					AuthorizationEndpoint: server.URL + "/authorize",
					TokenEndpoint:         server.URL + "/token",
					Scopes:                []string{"openid", "profile"},
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
					},
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	embed, err := NewEmbeddedAuthServerWithStorage(context.Background(), cfg, tracker)
	require.Error(t, err,
		"discovery returns 500, so DCR resolution must fail and the constructor must return an error")
	assert.Nil(t, embed,
		"failed constructor must return nil EmbeddedAuthServer")
	assert.Equal(t, int32(1), tracker.closeCount.Load(),
		"failed NewEmbeddedAuthServer must Close the storage exactly once via the deferred-cleanup gate; "+
			"a count of 0 indicates the deferred Close did not run, leaking the backend on the error path")
}

// TestEmbeddedAuthServer_DCRStorePersistsAcrossClose verifies that the DCR
// store reachable through EmbeddedAuthServer.DCRStore() holds the resolved
// RFC 7591 client registration after the constructor's full DCR resolver
// runs against a mock AS. The Get is issued BEFORE Close so the assertion
// does not depend on the (undocumented) MemoryStorage post-Close
// readability that an earlier version of this test silently relied on.
//
// What this test does cover:
//
//   - NewEmbeddedAuthServer runs the full DCR resolver against a mock AS
//     during construction, populating the storage-backed DCR store, and
//     surfaces the same storage.DCRCredentialStore the authserver itself
//     reads from via DCRStore(). The persisted credentials are readable
//     by issuing a Get against the captured store while the server is
//     still live.
//
// What this test does NOT cover (deferred follow-up):
//
//   - The full "boot, close, boot again on the same backend, observe zero
//     /register calls on the second boot" cross-restart scenario. Closing
//     that gap requires either miniredis-Sentinel emulation or a
//     Docker-based Redis Sentinel cluster in the test harness, since the
//     production restart path lives on Redis (Memory cannot be shared
//     across two NewEmbeddedAuthServer constructors). Tracked as a
//     follow-up; this test deliberately scopes itself to what is
//     exercisable today against the production constructor seam.
func TestEmbeddedAuthServer_DCRStorePersistsAcrossClose(t *testing.T) {
	t.Parallel()

	server, requestCount := newMockAuthorizationServer(t)

	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        server.URL,
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "dcr-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					ClientID:              "",
					AuthorizationEndpoint: server.URL + "/authorize",
					TokenEndpoint:         server.URL + "/token",
					Scopes:                []string{"openid", "profile"},
					// The mock IdP is a loopback listener, and a registration endpoint read
					// out of its metadata is dialed under the strict server-supplied policy.
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
					},
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	embed, err := NewEmbeddedAuthServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, embed)
	t.Cleanup(func() { _ = embed.Close() })

	firstBootRequests := atomic.LoadInt32(requestCount)
	require.Greater(t, firstBootRequests, int32(0),
		"first boot must have issued network I/O to the mock AS during DCR")

	// Capture the storage instance the constructor wired into the DCR
	// store. This is the same backend the authserver itself was using; in
	// production it is shared across authserver state, so DCR survives
	// restart on the same backend.
	persistentStore := embed.DCRStore()
	require.NotNil(t, persistentStore,
		"NewEmbeddedAuthServer must surface a storage-level DCRCredentialStore")

	// Verify the persisted DCR row by issuing a Get against the captured
	// store BEFORE closing the server. Doing the Get pre-Close avoids
	// silently depending on whichever storage backend the test happens to
	// use staying readable after Close (a contract MemoryStorage honors
	// today but RedisStorage's closed connection pool does not). The
	// assertion proves the persistence boundary the production cross-
	// replica and cross-restart reuse paths depend on: that the
	// resolution lives in storage, not in process-local cache state.
	redirectURI := server.URL + "/oauth/callback"
	key := storage.DCRKey{
		Issuer:      server.URL,
		UpstreamID:  server.URL,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	creds, err := persistentStore.GetDCRCredentials(context.Background(), key)
	require.NoError(t, err,
		"DCR credentials must be readable from the captured store — "+
			"this is the persistence boundary cross-replica reuse depends on")
	require.NotNil(t, creds)
	assert.Equal(t, "dcr-client-id", creds.ClientID,
		"persisted ClientID must match the first boot's DCR resolution")
	assert.Equal(t, "dcr-client-secret", creds.ClientSecret,
		"persisted ClientSecret must match the first boot's DCR resolution")

	// Mock-AS request count is unchanged after the survival check — the
	// Get is a pure store read with no upstream traffic.
	assert.Equal(t, firstBootRequests, atomic.LoadInt32(requestCount),
		"GetDCRCredentials must not issue any HTTP requests to the mock AS")
}

// urlErrorOnCloseStorage wraps an authserver storage and returns a fixed
// URL-bearing error from Close. It exists so
// TestNewEmbeddedAuthServer_DeferredCleanupSanitizesLog can verify that the
// deferred-cleanup gate routes both closeErr and retErr through
// dcr.SanitizeErrorForLog, scrubbing any query / userinfo / fragment that might
// carry credentials in a future regression.
type urlErrorOnCloseStorage struct {
	storage.Storage
	closeErr error
}

func (s *urlErrorOnCloseStorage) Close() error {
	// Intentionally drop the inner Close result: this test is about the log
	// path, not about double-closing the inner storage. Returning the
	// fixed error makes the slog capture deterministic.
	_ = s.Storage.Close()
	return s.closeErr
}

// syncBuffer is a concurrency-safe io.Writer over a bytes.Buffer, used by the
// slog-capturing tests below. slog.SetDefault is process-global, so while a
// capturing handler is installed, any goroutine in the test binary — not just
// the capturing test's own subtests — can write a record into it; a plain
// bytes.Buffer is a data race waiting for some concurrently running test to
// log, which is exactly what it became once a parallel parent was added.
// Mirrors the identically-named helper in pkg/authz/authorizers/cedar/
// core_test.go; kept as a separate copy since it is an unexported test type
// and not worth exporting across packages.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNewEmbeddedAuthServer_DeferredCleanupSanitizesLog pins the post-#5196
// invariant that the deferred-cleanup slog.Warn at the top of
// NewEmbeddedAuthServerWithStorage routes both closeErr and retErr through
// dcr.SanitizeErrorForLog, so a future regression that drops the call (or that
// changes the error chain to inline an upstream response body containing a
// userinfo/query/fragment) cannot silently leak secrets to operator logs.
//
// The test injects a closeErr containing a URL with a secret-bearing query
// (?token=leak-marker) and a discovery URL whose host appears verbatim in
// the wrapped DCR error chain (so the captured slog record's `cause` field
// also exercises the sanitiser). It then asserts:
//   - the captured log record does NOT contain the literal secret marker;
//   - the captured log record DOES contain the host components, so
//     operators retain enough context to correlate the failure.
//
// NOT t.Parallel(): the test swaps slog.Default() and restores it via
// t.Cleanup. The capturing buffer is a syncBuffer, so a concurrent writer is
// not itself a data race — the reason to stay sequential is the swap/restore
// pair, which two overlapping capturing tests would interleave, leaving
// whichever restored last as the default and clobbering the other's capture.
//
//nolint:paralleltest // see comment above; mutates the package-global slog.Default()
func TestNewEmbeddedAuthServer_DeferredCleanupSanitizesLog(t *testing.T) {
	const (
		closeErrSecretMarker = "close-leak-marker-7f9c"
		retErrSecretMarker   = "ret-leak-marker-3b2a"
	)

	closeErr := fmt.Errorf(
		"redis: connection broken: https://primary:hidden@redis.example.com/0?token=%s",
		closeErrSecretMarker,
	)
	tracker := &urlErrorOnCloseStorage{
		Storage:  storage.NewMemoryStorage(),
		closeErr: closeErr,
	}

	// Discovery endpoint returns 500 so the DCR resolver fails and the
	// constructor reaches the deferred-cleanup gate. The query string
	// embedded in the discovery URL is what the resolver wraps into its
	// error chain via fmt.Errorf("...%w", err) — so retErr's text is the
	// vehicle for the second secret marker.
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(httpServer.Close)

	discoveryURL := httpServer.URL + "/.well-known/oauth-authorization-server?token=" + retErrSecretMarker

	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        httpServer.URL,
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "dcr-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					ClientID:              "",
					AuthorizationEndpoint: httpServer.URL + "/authorize",
					TokenEndpoint:         httpServer.URL + "/token",
					Scopes:                []string{"openid", "profile"},
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL: discoveryURL,
					},
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	// Capture slog output by swapping the default logger for the duration
	// of this test. Restore on cleanup so parallel tests are unaffected.
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	embed, err := NewEmbeddedAuthServerWithStorage(context.Background(), cfg, tracker)
	require.Error(t, err)
	assert.Nil(t, embed)

	logged := buf.String()

	// Defense in depth: both secret markers must be stripped before the
	// Warn record reaches operator logs.
	assert.NotContains(t, logged, closeErrSecretMarker,
		"closeErr secret marker must be sanitised before reaching the Warn record")
	assert.NotContains(t, logged, retErrSecretMarker,
		"retErr secret marker must be sanitised before reaching the Warn record")
	assert.NotContains(t, logged, "primary:hidden",
		"closeErr userinfo must be sanitised before reaching the Warn record")

	// The host components survive sanitisation so operators retain enough
	// context to correlate the failure with upstream logs.
	assert.Contains(t, logged, "redis.example.com",
		"closeErr host must remain in the Warn record after sanitisation")
}

// TestNewEmbeddedAuthServer_TrustedIssuers pins the RunConfig.TrustedIssuers
// -> Config.TrustedIssuers conversion (embeddedauthserver.go's resolvedCfg,
// ~line 244). "Construction succeeds" alone doesn't discriminate: deleting
// the resolvedCfg.TrustedIssuers assignment entirely still builds a server
// (it degrades to the empty-TrustedIssuers case, which is itself valid), so
// the no-trusted-issuers control subtest can't disagree with a broken one.
//
// Instead this uses an observable side effect that only fires if the field
// actually reached the Factory closure and NewMultiIssuerTokenValidator's
// constructor (not merely RunConfig.Validate/Config.Validate, both of which
// run before resolvedCfg is built): NewMultiIssuerTokenValidator logs a
// slog.Warn ("Trusted issuer has no allowed actors or actor matcher
// configured") for each TrustedIssuer with an empty AllowedActors and no
// ActorMatcher. Confirmed by mutation: deleting
// `TrustedIssuers: slices.Clone(cfg.TrustedIssuers)` from resolvedCfg's
// construction makes this test fail (the warning never fires because
// MultiIssuerTokenValidator is never even built — Factory falls back to the
// self-issued validator).
//
// What this does NOT cover: resolvedCfg has no exported accessor, so the
// mutate-the-caller's-slice-after-construction claim (slices.Clone protects
// the outer []TrustedIssuer from a later append/removal on cfg, but is
// shallow — a mutation reaching through a still-shared AllowedActors slice
// would not be caught by this outer clone) is not observable through this
// package's public API. Proving it would require a live token-exchange HTTP
// round trip against an external issuer (Step 7's HTTP-level integration
// tests), or a production-code accessor this task's scope excludes.
// NewMultiIssuerTokenValidator's own AllowedActors clone (see
// multi_issuer_validator.go) closes that gap at the layer where it actually
// matters for concurrent request handling.
func TestNewEmbeddedAuthServer_TrustedIssuers(t *testing.T) {
	t.Parallel()

	base := func() *authserver.RunConfig {
		return &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "https://auth.example.com",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client-id",
						RedirectURI:           "https://auth.example.com/oauth/callback",
					},
				},
			},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	t.Run("no trusted issuers builds server (control case)", func(t *testing.T) {
		t.Parallel()

		cfg := base()

		srv, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, srv)
		require.NoError(t, srv.Close())
	})

	// NOT t.Parallel(): captures the package-global slog.Default() via
	// slog.SetDefault, which is atomic, and restores it via t.Cleanup.
	// The buffer itself is the only unsynchronized object in that swap, and
	// it's mutex-guarded (syncBuffer), so records emitted by any other test
	// running concurrently in this parallel phase land harmlessly. That's
	// fine here because the assertion below is Contains-only, which
	// tolerates foreign records mixed into the buffer.
	//nolint:paralleltest // see comment above
	t.Run("valid trusted issuers reach the Factory closure and validator constructor", func(t *testing.T) {
		cfg := base()
		cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{
			{
				IssuerURL:        "https://idp.example.com",
				ExpectedAudience: "https://mcp.example.com",
				// AllowedActors intentionally empty: this is what makes
				// NewMultiIssuerTokenValidator log its warning, the
				// observable proof this test relies on.
				AllowedDelegateClients: []string{"*"},
			},
		}

		var buf syncBuffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(prev) })

		srv, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, srv)
		t.Cleanup(func() { _ = srv.Close() })

		assert.Contains(t, buf.String(), "Trusted issuer has no allowed actors or actor matcher configured")
	})
}

// TestNewEmbeddedAuthServer_CanonicalInboundGrants pins the
// RunConfig.InboundGrants -> Config wiring added by
// authserver.NormalizeInboundGrants and prepareInboundGrantConfiguration: a
// canonical delegate client and SPIFFE client resolve through the same
// startup path as their legacy equivalents, canonical jwt_bearer policy
// reaches discovery, and inbound_grants.token_exchange being explicitly
// omitted (while another family is set) actually disables and stops
// advertising RFC 8693 rather than merely being validated in isolation
// (TestNormalizeInboundGrants, in pkg/authserver, already covers the pure
// normalization; this proves it reaches a running server).
func TestNewEmbeddedAuthServer_CanonicalInboundGrants(t *testing.T) {
	t.Parallel()

	base := func() *authserver.RunConfig {
		return &authserver.RunConfig{
			SchemaVersion: authserver.CurrentSchemaVersion,
			Issuer:        "https://auth.example.com",
			Upstreams: []authserver.UpstreamRunConfig{
				{
					Name: "test-upstream",
					Type: authserver.UpstreamProviderTypeOAuth2,
					OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
						AuthorizationEndpoint: "https://example.com/authorize",
						TokenEndpoint:         "https://example.com/token",
						ClientID:              "test-client-id",
						RedirectURI:           "https://auth.example.com/oauth/callback",
					},
				},
			},
			ScopesSupported:  []string{"openid"},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	discoveryGrantTypes := func(t *testing.T, srv *EmbeddedAuthServer) []string {
		t.Helper()
		handler, ok := srv.Routes()["/.well-known/oauth-authorization-server"]
		require.True(t, ok, "discovery route must be registered")

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var metadata oauthproto.AuthorizationServerMetadata
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&metadata))
		return metadata.GrantTypesSupported
	}

	t.Run("canonical delegate client alone builds a server with token exchange enabled", func(t *testing.T) {
		t.Parallel()

		secretFile := filepath.Join(t.TempDir(), "delegate-secret")
		require.NoError(t, os.WriteFile(secretFile, []byte("s3cr3t-value-that-is-long-enough-for-32-chars"), 0o600))

		cfg := base()
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
				DelegateClients: []authserver.DelegateClientRunConfig{{
					ClientID:         "coding-agent",
					ClientSecretFile: secretFile,
					Scopes:           []string{"openid"},
					Audiences:        []string{"https://mcp.example.com"},
				}},
			},
		}

		srv, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, srv)
		t.Cleanup(func() { _ = srv.Close() })

		assert.Contains(t, discoveryGrantTypes(t, srv), oauthproto.GrantTypeTokenExchange,
			"canonical delegate client must not disable token exchange")
	})

	// A SPIFFE-only configuration cannot be exercised through
	// NewEmbeddedAuthServer here: RunConfig.Validate() hard-rejects a
	// non-empty spiffe_trust_domains until a real SVID-verification
	// consumer lands (see config.go's validateSPIFFENotYetEnforced). This
	// test instead calls prepareInboundGrantConfiguration directly -- the
	// package-private function NewEmbeddedAuthServer would otherwise reach
	// after cfg.Validate() -- to prove the underlying capability logic
	// still holds: SPIFFE client-auth presence must force
	// Capabilities.TokenExchange true independent of the legacy/canonical
	// token-exchange projection, so a SPIFFE-only config does not silently
	// disable the RFC 8693 grant handler once the hard-reject above is
	// lifted.
	t.Run("SPIFFE client auth alone sets the token exchange capability", func(t *testing.T) {
		t.Parallel()

		cfg := base()
		cfg.SPIFFETrustDomains = []authserver.SPIFFETrustDomainRunConfig{{
			Name:        "prod",
			TrustDomain: "example.org",
			Methods:     []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			BundleSource: authserver.SPIFFEBundleSourceRunConfig{
				Type:        authserver.SPIFFEBundleSourceTypeWorkloadAPI,
				WorkloadAPI: &authserver.SPIFFEWorkloadAPIBundleSourceRunConfig{},
			},
		}}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
				TrustDomainRef:   "prod",
				PrincipalPattern: "spiffe://example.org/ns/default/agent",
				ClientID:         "spiffe-agent",
				Methods:          []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
				Scopes:           []string{"openid"},
				Audiences:        []string{"https://mcp.example.com"},
				GrantTypes:       []string{authserver.SPIFFEGrantTypeTokenExchange},
			}},
		}

		normalized, _, spiffeTrust, err := prepareInboundGrantConfiguration(cfg, nil)
		require.NoError(t, err)
		require.NotNil(t, spiffeTrust)
		assert.True(t, normalized.Capabilities.TokenExchange,
			"SPIFFE client auth alone must not leave the token exchange capability disabled")
	})

	t.Run("inbound_grants with token_exchange omitted disables and stops advertising RFC 8693", func(t *testing.T) {
		t.Parallel()

		cfg := base()
		cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{{Name: "idp", IssuerURL: "https://idp.example.com"}}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			JWTBearer: &authserver.JWTBearerInboundGrantRunConfig{
				IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
					IssuerRef:       "idp",
					MaxAssertionAge: "5m",
					SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
						Subject:          "workload",
						AllowedResources: []string{"https://mcp.example.com"},
					}},
				}},
			},
		}

		srv, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.NoError(t, err)
		require.NotNil(t, srv)
		t.Cleanup(func() { _ = srv.Close() })

		grantTypes := discoveryGrantTypes(t, srv)
		assert.NotContains(t, grantTypes, oauthproto.GrantTypeTokenExchange,
			"inbound_grants present with token_exchange omitted must disable RFC 8693")
		assert.Contains(t, grantTypes, oauthproto.GrantTypeJWTBearer,
			"canonical jwt_bearer policy must still be advertised")
	})

	t.Run("canonical token exchange conflicts with legacy delegate_clients", func(t *testing.T) {
		t.Parallel()

		cfg := base()
		cfg.DelegateClients = []authserver.DelegateClientRunConfig{{
			ClientID:  "legacy-delegate",
			Scopes:    []string{"openid"},
			Audiences: []string{"https://mcp.example.com"},
		}}
		cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
			TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{},
		}

		_, err := NewEmbeddedAuthServer(context.Background(), cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with legacy delegate_clients")
	})
}

func TestResolveCIMDConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns zero values", func(t *testing.T) {
		t.Parallel()
		enabled, size, ttl := resolveCIMDConfig(nil)
		assert.False(t, enabled)
		assert.Zero(t, size)
		assert.Zero(t, ttl)
	})

	t.Run("non-nil input passes values through", func(t *testing.T) {
		t.Parallel()
		cfg := &authserver.CIMDRunConfig{
			Enabled:          true,
			CacheMaxSize:     128,
			CacheFallbackTTL: "10m",
		}
		enabled, size, ttl := resolveCIMDConfig(cfg)
		assert.True(t, enabled)
		assert.Equal(t, 128, size)
		assert.Equal(t, 10*time.Minute, ttl)
	})
}

//nolint:tparallel // t.Setenv mutates the process-wide environment.
func TestResolveDelegateClients(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "delegate-secret")
	emptySecretFile := filepath.Join(t.TempDir(), "empty-delegate-secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("  file-secret\n"), 0600))
	require.NoError(t, os.WriteFile(emptySecretFile, nil, 0600))
	t.Setenv("TEST_DELEGATE_SECRET", "env-secret")
	t.Setenv("TEST_EMPTY_DELEGATE_SECRET", "")

	tests := []struct {
		name    string
		clients []authserver.DelegateClientRunConfig
		want    string
		wantErr string
	}{
		{
			name: "file takes precedence and is trimmed",
			clients: []authserver.DelegateClientRunConfig{{
				ClientID: "delegate", ClientSecretFile: secretFile, ClientSecretEnvVar: "TEST_DELEGATE_SECRET",
				Scopes: []string{"openid"}, Audiences: []string{"audience"},
			}},
			want: "file-secret",
		},
		{
			name: "environment secret",
			clients: []authserver.DelegateClientRunConfig{{
				ClientID: "delegate", ClientSecretEnvVar: "TEST_DELEGATE_SECRET",
				Scopes: []string{"openid"}, Audiences: []string{"audience"},
			}},
			want: "env-secret",
		},
		{
			name:    "empty file is rejected",
			clients: []authserver.DelegateClientRunConfig{{ClientID: "delegate", ClientSecretFile: emptySecretFile}},
			wantErr: "resolved client secret is empty",
		},
		{
			name:    "unreadable file is rejected",
			clients: []authserver.DelegateClientRunConfig{{ClientID: "delegate", ClientSecretFile: filepath.Join(t.TempDir(), "missing")}},
			wantErr: "failed to read secret file",
		},
		{
			name:    "missing references are rejected",
			clients: []authserver.DelegateClientRunConfig{{ClientID: "delegate"}},
			wantErr: "resolved client secret is empty",
		},
		{
			name:    "empty environment is rejected",
			clients: []authserver.DelegateClientRunConfig{{ClientID: "delegate", ClientSecretEnvVar: "TEST_EMPTY_DELEGATE_SECRET"}},
			wantErr: "is not set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveDelegateClients(tt.clients)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "file-secret")
				assert.NotContains(t, err.Error(), "env-secret")
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tt.want, got[0].ClientSecret)
		})
	}
}

func TestResolveDelegateClients_ClonesPermissions(t *testing.T) {
	clients := []authserver.DelegateClientRunConfig{{
		ClientID: "delegate", ClientSecretEnvVar: "TEST_DELEGATE_CLONE_SECRET",
		Scopes: []string{"openid"}, Audiences: []string{"audience"},
	}}
	t.Setenv("TEST_DELEGATE_CLONE_SECRET", "secret")

	resolved, err := resolveDelegateClients(clients)
	require.NoError(t, err)
	clients[0].Scopes[0] = "changed"
	clients[0].Audiences[0] = "changed"

	assert.Equal(t, "openid", resolved[0].Scopes[0])
	assert.Equal(t, "audience", resolved[0].Audiences[0])
}

// TestCloseIdleConnectionsDelegates pins that the wrapper forwards to the
// underlying server rather than no-opping or calling Close. This is the method
// runner-path embedders reach for when retiring a superseded server, and an
// empty body here would pass every other test in the package.
func TestCloseIdleConnectionsDelegates(t *testing.T) {
	t.Parallel()

	stub := &stubServer{}
	eas := &EmbeddedAuthServer{server: stub}

	eas.CloseIdleConnections()
	assert.Equal(t, int32(1), stub.idleCloses.Load())

	// Safe after Close: Close is sync.Once-guarded and draining an already
	// closed client's pool is a no-op, so a retire-then-shutdown sequence
	// cannot double-close anything.
	require.NoError(t, eas.Close())
	eas.CloseIdleConnections()
	assert.Equal(t, int32(2), stub.idleCloses.Load())
}

// TestCloseIdleConnectionsWithoutCapability pins that the wrapper degrades
// quietly when the underlying Server does not implement the optional capability,
// and in particular that it does not fall back to Close — the two are separate
// precisely because Close also tears down storage.
func TestCloseIdleConnectionsWithoutCapability(t *testing.T) {
	t.Parallel()

	stub := &capabilityFreeStubServer{}
	eas := &EmbeddedAuthServer{server: stub}

	assert.NotPanics(t, eas.CloseIdleConnections)
	assert.Zero(t, stub.closed.Load(), "must not fall back to Close")
}
