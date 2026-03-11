// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

func TestResolveAuthStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		cfg            *config.Config
		wantAuthStatus string
		wantAuthType   string
	}{
		{
			name:           "nil config",
			cfg:            nil,
			wantAuthStatus: "none",
			wantAuthType:   "",
		},
		{
			name:           "empty auth type",
			cfg:            &config.Config{},
			wantAuthStatus: "none",
			wantAuthType:   "",
		},
		{
			name: "oauth with nil OAuth config",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type:  config.RegistryAuthTypeOAuth,
					OAuth: nil,
				},
			},
			wantAuthStatus: "configured",
			wantAuthType:   "oauth",
		},
		{
			name: "oauth with empty CachedRefreshTokenRef",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type: config.RegistryAuthTypeOAuth,
					OAuth: &config.RegistryOAuthConfig{
						CachedRefreshTokenRef: "",
					},
				},
			},
			wantAuthStatus: "configured",
			wantAuthType:   "oauth",
		},
		{
			name: "oauth with cached refresh token",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type: config.RegistryAuthTypeOAuth,
					OAuth: &config.RegistryOAuthConfig{
						CachedRefreshTokenRef: "REGISTRY_OAUTH_abc123",
					},
				},
			},
			wantAuthStatus: "authenticated",
			wantAuthType:   "oauth",
		},
		{
			name: "unknown auth type",
			cfg: &config.Config{
				RegistryAuth: config.RegistryAuth{
					Type: "api-key",
				},
			},
			wantAuthStatus: "configured",
			wantAuthType:   "api-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, authType := resolveAuthStatus(tt.cfg)
			require.Equal(t, tt.wantAuthStatus, status)
			require.Equal(t, tt.wantAuthType, authType)
		})
	}
}

func TestIsRegistryAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "direct ErrRegistryAuthRequired",
			err:  auth.ErrRegistryAuthRequired,
			want: true,
		},
		{
			name: "wrapped ErrRegistryAuthRequired",
			err:  fmt.Errorf("failed to get provider: %w", auth.ErrRegistryAuthRequired),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("random error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isRegistryAuthError(tt.err))
		})
	}
}

func TestWriteRegistryAuthRequiredError(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	writeRegistryAuthRequiredError(w)

	result := w.Result()
	defer result.Body.Close()

	// Verify status code is 503 Service Unavailable
	require.Equal(t, http.StatusServiceUnavailable, result.StatusCode)

	// Verify Content-Type header
	require.Equal(t, "application/json", result.Header.Get("Content-Type"))

	// Verify JSON body structure
	var body registryAuthErrorResponse
	err := json.NewDecoder(result.Body).Decode(&body)
	require.NoError(t, err)
	require.Equal(t, RegistryAuthRequiredCode, body.Code)
	require.NotEmpty(t, body.Message)
}
