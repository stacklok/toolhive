// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	secretmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

func newTestSecretProvider(t *testing.T, values map[string]string) *secretmocks.MockProvider {
	t.Helper()

	ctrl := gomock.NewController(t)
	provider := secretmocks.NewMockProvider(ctrl)
	provider.EXPECT().GetSecret(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, name string) (string, error) {
			value, ok := values[name]
			if !ok {
				return "", fmt.Errorf("secret %q not found", name)
			}
			return value, nil
		},
	).AnyTimes()
	return provider
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
		{
			name:    "root path URI is rejected",
			uri:     "https://example.com/",
			wantErr: true,
		},
		{
			name:    "non-canonical root path URI is rejected",
			uri:     "https://example.com//",
			wantErr: true,
		},
		{
			name:    "missing host is rejected",
			uri:     "https:///oauth/register/client-id",
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
		err := h.renewClientSecret(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "registration_client_uri missing")
	})

	t.Run("missing registration_token_ref", func(t *testing.T) {
		t.Parallel()

		h := &Handler{
			config: &Config{
				CachedRegClientURI: "https://example.com/register/client-id",
				CachedRegTokenRef:  "",
			},
		}
		err := h.renewClientSecret(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "registration_access_token missing")
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
		err := h.renewClientSecret(context.Background(), "")
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

		var updateReq clientUpdateRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&updateReq))
		assert.Equal(t, []string{"http://localhost:9876/callback"}, updateReq.RedirectURIs)

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
	t.Cleanup(server.Close)

	// Set up persister capture
	var persistedClientID, persistedSecret, persistedRegToken, persistedRegURI string
	var persistedExpiry time.Time

	h := &Handler{
		config: &Config{
			CachedClientID:        "test-client-id",
			CachedRegClientURI:    server.URL + "/register/test-client-id",
			CachedRegTokenRef:     "reg-token-secret-ref",
			CallbackPort:          8666,
			CachedDCRCallbackPort: 9876,
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"reg-token-secret-ref": "reg-access-token",
		}),
		clientCredentialsPersister: func(
			clientID, secret string,
			expiry time.Time,
			regToken, regURI, _ string,
			callbackPort int,
		) error {
			persistedClientID = clientID
			persistedSecret = secret
			persistedExpiry = expiry
			persistedRegToken = regToken
			persistedRegURI = regURI
			assert.Equal(t, 9876, callbackPort)
			return nil
		},
	}

	err := h.renewClientSecret(context.Background(), server.URL)
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
	t.Cleanup(server.Close)

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"reg-token-ref": "bad-token",
		}),
		clientCredentialsPersister: func(_, _ string, _ time.Time, _, _, _ string, _ int) error {
			return nil
		},
	}

	err := h.renewClientSecret(context.Background(), server.URL)
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
	t.Cleanup(server.Close)

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"reg-token-ref": "some-token",
		}),
		clientCredentialsPersister: nil, // no persister
	}

	err := h.renewClientSecret(context.Background(), server.URL)
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
	t.Cleanup(server.Close)

	var capturedExpiry time.Time

	h := &Handler{
		config: &Config{
			CachedClientID:     "test-client-id",
			CachedRegClientURI: server.URL + "/register/test-client-id",
			CachedRegTokenRef:  "reg-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"reg-token-ref": "some-token",
		}),
		clientCredentialsPersister: func(_, _ string, expiry time.Time, _, _, _ string, _ int) error {
			capturedExpiry = expiry
			return nil
		},
	}

	err := h.renewClientSecret(context.Background(), server.URL)
	require.NoError(t, err)
	assert.True(t, capturedExpiry.IsZero(), "zero client_secret_expires_at must produce zero time.Time")
}

func TestRenewClientSecret_MalformedJSON(t *testing.T) {
	t.Parallel()

	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid-json`))
	}))
	t.Cleanup(svc.Close)

	h := &Handler{
		config: &Config{
			CachedRegClientURI: svc.URL + "/register/test-client-id",
			CachedRegTokenRef:  "rat-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{"rat-ref": "rat-token"}),
	}

	err := h.renewClientSecret(context.Background(), svc.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode client update response")
}

func TestRenewClientSecret_MissingFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		wantErr  string
	}{
		{
			name:     "missing_client_id",
			response: `{"client_secret": "new-secret"}`,
			wantErr:  "client update response missing client_id",
		},
		{
			name:     "missing_client_secret",
			response: `{"client_id": "test-client-id"}`,
			wantErr:  "client update response missing client_secret",
		},
	}

	for _, tt := range tests {
		tt := tt // capture loop variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.response))
			}))
			t.Cleanup(svc.Close)

			h := &Handler{
				config: &Config{
					CachedRegClientURI: svc.URL + "/register/test-client-id",
					CachedRegTokenRef:  "rat-ref",
				},
				secretProvider: newTestSecretProvider(t, map[string]string{"rat-ref": "rat-token"}),
			}

			err := h.renewClientSecret(context.Background(), svc.URL)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestHandler_Restore_RenewSuccess(t *testing.T) {
	t.Parallel()

	// Initial setup: secret expiring in 1 hour
	expiry := time.Now().Add(1 * time.Hour)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"client_id": "test-client", "client_secret": "new-secret", "client_secret_expires_at": 4102444800}`))
	}))
	t.Cleanup(svc.Close)

	var persistedID, persistedSecret string
	renewalRequests := 0
	h := &Handler{
		config: &Config{
			CachedClientID:        "test-client",
			CachedSecretExpiry:    expiry,
			CachedRegClientURI:    svc.URL + "/register/test-client",
			CachedRegTokenRef:     "rat-ref",
			CachedRefreshTokenRef: "refresh-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"rat-ref":           "rat-token",
			"refresh-token-ref": "some-refresh-token",
		}),
		clientCredentialsPersister: func(id, secret string, _ time.Time, _, _, _ string, _ int) error {
			persistedID = id
			persistedSecret = secret
			renewalRequests++
			return nil
		},
	}

	// Calling tryRestoreFromCachedTokens should trigger renewal because of the 1h expiry.
	// We expect an error because it will try to refresh the token and fail (no token endpoint).
	_, err := h.tryRestoreFromCachedTokens(context.Background(), svc.URL, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cached tokens are invalid or expired")

	// But renewal DID happen
	assert.Equal(t, "test-client", persistedID)
	assert.Equal(t, "new-secret", persistedSecret)
	assert.Equal(t, 1, renewalRequests)
	assert.False(t, h.isSecretExpiredOrExpiringSoon())
}

func TestHandler_Restore_RenewFail_Soft(t *testing.T) {
	t.Parallel()

	// Initial setup: secret expiring in 1 hour
	expiry := time.Now().Add(1 * time.Hour)
	var renewalPUTs atomic.Int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			renewalPUTs.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(svc.Close)

	h := &Handler{
		config: &Config{
			CachedClientID:        "test-client",
			CachedSecretExpiry:    expiry,
			CachedRegClientURI:    svc.URL + "/register/test-client",
			CachedRegTokenRef:     "rat-ref",
			CachedRefreshTokenRef: "refresh-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"rat-ref":           "rat-token",
			"refresh-token-ref": "some-refresh-token",
		}),
		clientCredentialsPersister: func(_, _ string, _ time.Time, _, _, _ string, _ int) error { return nil },
	}

	// Renewal fails, but since it's only "expiring soon", each restore should
	// continue to token refresh after making exactly one renewal request.
	for attempt := int32(1); attempt <= 2; attempt++ {
		_, err := h.tryRestoreFromCachedTokens(context.Background(), svc.URL, nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cached tokens are invalid or expired")
		assert.Equal(t, attempt, renewalPUTs.Load())
	}
}

func TestHandler_Restore_RenewFail_Hard(t *testing.T) {
	t.Parallel()

	// Initial setup: secret already expired
	expiry := time.Now().Add(-1 * time.Hour)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(svc.Close)

	h := &Handler{
		config: &Config{
			CachedClientID:        "test-client",
			CachedSecretExpiry:    expiry,
			CachedRegClientURI:    svc.URL + "/register/test-client",
			CachedRegTokenRef:     "rat-ref",
			CachedRefreshTokenRef: "refresh-token-ref",
		},
		secretProvider: newTestSecretProvider(t, map[string]string{
			"rat-ref":           "rat-token",
			"refresh-token-ref": "some-refresh-token",
		}),
		clientCredentialsPersister: func(string, string, time.Time, string, string, string, int) error { return nil },
	}

	// Renewal fails and it's fully expired -> fatal error
	_, err := h.tryRestoreFromCachedTokens(context.Background(), svc.URL, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client secret expired at")
	assert.Contains(t, err.Error(), "and renewal failed")
}

func TestPersistRenewedSecret_RegistrationClientURIRotation(t *testing.T) {
	t.Parallel()

	const currentURI = "https://issuer.example/register/test-client"
	tests := []struct {
		name       string
		rotatedURI string
		wantErr    string
	}{
		{
			name:       "same origin is accepted",
			rotatedURI: "https://issuer.example/manage/test-client",
		},
		{
			name:       "equivalent default port is accepted",
			rotatedURI: "https://issuer.example:443/manage/test-client",
		},
		{
			name:       "cross origin is rejected",
			rotatedURI: "https://attacker.example/manage/test-client",
			wantErr:    "must remain on origin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			persistCalls := 0
			persistedURI := ""
			h := &Handler{
				config: &Config{CachedRegClientURI: currentURI},
				clientCredentialsPersister: func(_, _ string, _ time.Time, _, regURI, _ string, _ int) error {
					persistCalls++
					persistedURI = regURI
					return nil
				},
			}

			err := h.persistRenewedSecret(clientUpdateResponse{
				ClientID:              "test-client",
				ClientSecret:          "rotated-secret",
				RegistrationClientURI: tt.rotatedURI,
			})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Zero(t, persistCalls)
				assert.Equal(t, currentURI, h.config.CachedRegClientURI)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, 1, persistCalls)
			assert.Equal(t, tt.rotatedURI, persistedURI)
			assert.Equal(t, tt.rotatedURI, h.config.CachedRegClientURI)
		})
	}
}

func TestNewRenewalHTTPClient(t *testing.T) {
	t.Parallel()

	baseClient := oauthproto.DefaultHTTPClient()
	baseTransport, ok := baseClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, baseTransport.Proxy)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	localhostClient := newRenewalHTTPClient(
		context.Background(),
		server.URL,
	)
	assert.Equal(t, baseClient.Timeout, localhostClient.Timeout)
	assert.Same(t, baseTransport, localhostClient.Transport)
	require.NotNil(t, localhostClient.CheckRedirect)

	resp, err := localhostClient.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	originalRequest, err := http.NewRequest(http.MethodGet, "https://issuer.example/register", nil)
	require.NoError(t, err)
	sameHostRequest, err := http.NewRequest(http.MethodGet, "https://issuer.example/rotated", nil)
	require.NoError(t, err)
	crossHostRequest, err := http.NewRequest(http.MethodGet, "https://attacker.example/rotated", nil)
	require.NoError(t, err)
	assert.NoError(t, localhostClient.CheckRedirect(sameHostRequest, []*http.Request{originalRequest}))
	assert.ErrorIs(t,
		localhostClient.CheckRedirect(crossHostRequest, []*http.Request{originalRequest}),
		networking.ErrRedirectRefused,
	)

	publicClient := newRenewalHTTPClient(
		context.Background(),
		"https://issuer.example",
	)
	assert.Equal(t, baseClient.Timeout, publicClient.Timeout)
	protectedTransport, ok := publicClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, baseTransport, protectedTransport)
	assert.Nil(t, protectedTransport.Proxy)
	assert.NotNil(t, protectedTransport.DialContext)
	assert.True(t, protectedTransport.DisableKeepAlives)
	assert.Equal(t, baseTransport.TLSHandshakeTimeout, protectedTransport.TLSHandshakeTimeout)
	assert.False(t, baseTransport.DisableKeepAlives, "the shared OAuth transport must not be mutated")

	_, err = publicClient.Get(server.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), networking.ErrPrivateIpAddress)
}
