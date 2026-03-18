// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
)

// mockSecretProvider is a simple in-memory secret store for tests.
// It implements the full secrets.Provider interface.
type mockSecretProvider struct {
	secrets map[string]string
}

func newMockSecretProvider(initial map[string]string) *mockSecretProvider {
	if initial == nil {
		initial = make(map[string]string)
	}
	return &mockSecretProvider{secrets: initial}
}

func (m *mockSecretProvider) GetSecret(_ context.Context, name string) (string, error) {
	v, ok := m.secrets[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return v, nil
}

func (m *mockSecretProvider) SetSecret(_ context.Context, name, value string) error {
	m.secrets[name] = value
	return nil
}

func (m *mockSecretProvider) DeleteSecret(_ context.Context, name string) error {
	delete(m.secrets, name)
	return nil
}

func (m *mockSecretProvider) ListSecrets(_ context.Context) ([]secrets.SecretDescription, error) {
	result := make([]secrets.SecretDescription, 0, len(m.secrets))
	for k := range m.secrets {
		result = append(result, secrets.SecretDescription{Key: k})
	}
	return result, nil
}

func (*mockSecretProvider) Cleanup() error { return nil }

func (*mockSecretProvider) Capabilities() secrets.ProviderCapabilities {
	return secrets.ProviderCapabilities{
		CanRead:   true,
		CanWrite:  true,
		CanDelete: true,
		CanList:   true,
	}
}

// TestIsSecretExpiredOrExpiringSoon tests the expiry helper on various time scenarios.
func TestIsSecretExpiredOrExpiringSoon(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expiry      time.Time
		wantExpired bool
	}{
		{
			name:        "zero expiry means never expires",
			expiry:      time.Time{},
			wantExpired: false,
		},
		{
			name:        "expiry far in the future — not expiring",
			expiry:      time.Now().Add(48 * time.Hour),
			wantExpired: false,
		},
		{
			name:        "expiry within 24h buffer — expiring soon",
			expiry:      time.Now().Add(12 * time.Hour),
			wantExpired: true,
		},
		{
			name:        "expiry in the past — already expired",
			expiry:      time.Now().Add(-1 * time.Hour),
			wantExpired: true,
		},
		{
			name:        "expiry exactly at buffer boundary — expiring soon",
			expiry:      time.Now().Add(secretExpiryBuffer - time.Minute),
			wantExpired: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{
				config: &Config{
					CachedSecretExpiry: tt.expiry,
				},
			}
			assert.Equal(t, tt.wantExpired, h.isSecretExpiredOrExpiringSoon())
		})
	}
}

// TestValidateRegistrationClientURI tests URI validation.
func TestValidateRegistrationClientURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uri     string
		wantErr bool
	}{
		{
			name:    "empty URI",
			uri:     "",
			wantErr: true,
		},
		{
			name:    "valid HTTPS URI",
			uri:     "https://example.com/oauth/register/client-id",
			wantErr: false,
		},
		{
			name:    "HTTP URI for non-localhost is rejected",
			uri:     "http://example.com/oauth/register/client-id",
			wantErr: true,
		},
		{
			name:    "localhost HTTP is allowed (development)",
			uri:     "http://localhost:8080/oauth/register/client-id",
			wantErr: false,
		},
		{
			name:    "127.0.0.1 HTTP is allowed (development)",
			uri:     "http://127.0.0.1:8080/oauth/register/client-id",
			wantErr: false,
		},
		{
			name:    "invalid URL",
			uri:     "://bad-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateRegistrationClientURI(tt.uri)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRenewClientSecret_MissingConfig tests early-exit conditions.
func TestRenewClientSecret_MissingConfig(t *testing.T) {
	t.Parallel()

	t.Run("missing registration_client_uri", func(t *testing.T) {
		t.Parallel()

		h := &Handler{
			config: &Config{
				CachedRegClientURI: "",
				CachedRegTokenRef:  "some-ref",
			},
		}
		err := h.renewClientSecret(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "registration_client_uri not available")
	})

	t.Run("missing registration_token_ref", func(t *testing.T) {
		t.Parallel()

		h := &Handler{
			config: &Config{
				CachedRegClientURI: "https://example.com/register/client-id",
				CachedRegTokenRef:  "",
			},
		}
		err := h.renewClientSecret(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "registration_access_token not available")
	})

	t.Run("missing secret provider", func(t *testing.T) {
		t.Parallel()

		h := &Handler{
			config: &Config{
				CachedRegClientURI: "https://example.com/register/client-id",
				CachedRegTokenRef:  "some-ref",
			},
			secretProvider: nil, // no provider
		}
		err := h.renewClientSecret(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "secret provider not configured")
	})
}

// TestRenewClientSecret_Success tests the happy path with a mock RFC 7592 server.
func TestRenewClientSecret_Success(t *testing.T) {
	t.Parallel()

	newSecret := "new-client-secret-xyz"
	newExpiry := time.Now().Add(24 * time.Hour * 30).Unix()
	newRegToken := "new-registration-access-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RFC 7592 §2.2: must be PUT with Bearer auth
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer reg-access-token")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Return the updated registration response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"client_id":                 "test-client-id",
			"client_secret":             newSecret,
			"client_secret_expires_at":  newExpiry,
			"registration_access_token": newRegToken,
			"registration_client_uri":   "http://" + r.Host + r.URL.Path,
		})
	}))
	defer server.Close()

	// Set up persister capture
	var persistedClientID, persistedSecret, persistedRegToken, persistedRegURI string
	var persistedExpiry time.Time

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-secret-ref",
		},
		secretProvider: newMockSecretProvider(map[string]string{
			"reg-token-secret-ref": "reg-access-token",
		}),
		clientCredentialsPersister: func(
			clientID, secret string,
			expiry time.Time,
			regToken, regURI string,
		) error {
			persistedClientID = clientID
			persistedSecret = secret
			persistedExpiry = expiry
			persistedRegToken = regToken
			persistedRegURI = regURI
			return nil
		},
	}

	err := h.renewClientSecret(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "test-client-id", persistedClientID)
	assert.Equal(t, newSecret, persistedSecret)
	assert.Equal(t, newRegToken, persistedRegToken)
	assert.False(t, persistedExpiry.IsZero(), "expiry should be set")
	assert.NotEmpty(t, persistedRegURI)
}

// TestRenewClientSecret_ServerError tests error propagation when the server returns non-200.
func TestRenewClientSecret_ServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newMockSecretProvider(map[string]string{
			"reg-token-ref": "bad-token",
		}),
		clientCredentialsPersister: func(_, _ string, _ time.Time, _, _ string) error {
			return nil
		},
	}

	err := h.renewClientSecret(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// TestRenewClientSecret_NoPersister tests failure when persister is not set.
func TestRenewClientSecret_NoPersister(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"client_id":     "test-client-id",
			"client_secret": "new-secret",
		})
	}))
	defer server.Close()

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newMockSecretProvider(map[string]string{
			"reg-token-ref": "some-token",
		}),
		clientCredentialsPersister: nil, // no persister
	}

	err := h.renewClientSecret(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client credentials persister not configured")
}

// TestRenewClientSecret_ZeroExpiryInResponse tests that a zero client_secret_expires_at
// is correctly interpreted as a non-expiring secret.
func TestRenewClientSecret_ZeroExpiryInResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"client_id":                "test-client-id",
			"client_secret":            "new-secret",
			"client_secret_expires_at": 0, // never expires
		})
	}))
	defer server.Close()

	var capturedExpiry time.Time

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newMockSecretProvider(map[string]string{
			"reg-token-ref": "some-token",
		}),
		clientCredentialsPersister: func(_, _ string, expiry time.Time, _, _ string) error {
			capturedExpiry = expiry
			return nil
		},
	}

	err := h.renewClientSecret(context.Background())
	require.NoError(t, err)
	assert.True(t, capturedExpiry.IsZero(), "zero client_secret_expires_at must produce zero time.Time")
}
