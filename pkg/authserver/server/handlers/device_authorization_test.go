// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestGenerateUserCode(t *testing.T) {
	t.Parallel()

	for i := 0; i < 200; i++ {
		code, err := generateUserCode()
		require.NoError(t, err)

		parts := strings.Split(code, "-")
		require.Len(t, parts, 2, "code must be in XXXX-XXXX format: %q", code)
		assert.Len(t, parts[0], userCodeGroupLength)
		assert.Len(t, parts[1], userCodeGroupLength)

		for _, c := range parts[0] + parts[1] {
			assert.Contains(t, userCodeCharset, string(c), "character %q not in userCodeCharset", c)
		}
	}
}

func TestGenerateUserCode_UniformOverCharset(t *testing.T) {
	t.Parallel()

	// With rejection sampling, every generated character must be drawn
	// uniformly from userCodeCharset -- i.e. every charset character is
	// reachable and no character is structurally favored. This does not
	// prove uniformity statistically (that would require far more samples
	// than is reasonable for a unit test), but it guards against a
	// regression back to the modulo-biased `b % charsetLen` reduction, which
	// would still pass the format checks above.
	const samples = 20000
	counts := make(map[byte]int, len(userCodeCharset))
	for i := 0; i < samples; i++ {
		code, err := generateUserCode()
		require.NoError(t, err)
		for _, c := range strings.ReplaceAll(code, "-", "") {
			counts[byte(c)]++
		}
	}

	require.Len(t, counts, len(userCodeCharset), "every charset character must appear across enough samples")

	total := samples * userCodeGroupLength * 2
	expected := float64(total) / float64(len(userCodeCharset))
	for c, n := range counts {
		// Loose bound: catch a gross bias (e.g. reintroducing '%' reduction),
		// not assert exact uniformity.
		assert.InDeltaf(t, expected, float64(n), expected*0.25,
			"character %q occurred %d times, expected close to %.0f", string(c), n, expected)
	}
}

// TestDeviceAuthorizationHandler_ClientAuthentication exercises the fix for
// RFC 8628 Section 3.1's requirement that /oauth/device_authorization
// authenticate clients "using the same method that the [token endpoint]
// supports": a confidential client must not be able to mint a device_code
// without proving its secret, while a public client continues to identify by
// client_id alone.
func TestDeviceAuthorizationHandler_ClientAuthentication(t *testing.T) {
	t.Parallel()

	const correctSecret = "s3cret-value"
	hashed, err := bcrypt.GenerateFromPassword([]byte(correctSecret), bcrypt.DefaultCost)
	require.NoError(t, err)

	stor := storage.NewMemoryStorage()
	ctx := context.Background()

	confidentialClient := &fosite.DefaultClient{
		ID:         "confidential-device-client",
		Secret:     hashed,
		GrantTypes: []string{oauthproto.GrantTypeDeviceCode},
		Scopes:     []string{"openid"},
		Public:     false,
	}
	require.NoError(t, stor.RegisterClient(ctx, confidentialClient))

	publicClient := &fosite.DefaultClient{
		ID:         "public-device-client",
		GrantTypes: []string{oauthproto.GrantTypeDeviceCode},
		Scopes:     []string{"openid"},
		Public:     true,
	}
	require.NoError(t, stor.RegisterClient(ctx, publicClient))

	fositeConfig := &fosite.Config{}
	provider := fosite.NewOAuth2Provider(stor, fositeConfig)
	// fosite.Config.GetSecretsHasher lazily initializes ClientSecretsHasher on
	// first call, with no synchronization -- calling it once here, before any
	// parallel subtest below can race on that lazy init, forces the
	// initialization to happen single-threaded so every subsequent read (from
	// any goroutine) sees an already-populated field.
	fositeConfig.GetSecretsHasher(ctx)

	config := &server.AuthorizationServerConfig{
		Config:            fositeConfig,
		ScopesSupported:   []string{"openid"},
		DeviceFlowEnabled: true,
	}
	h, err := NewHandler(provider, config, stor, nil)
	require.NoError(t, err)

	tests := []struct {
		name       string
		form       url.Values
		wantStatus int
		wantError  string
	}{
		{
			name:       "confidential client without secret is rejected",
			form:       url.Values{"client_id": {"confidential-device-client"}, "scope": {"openid"}},
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid_client",
		},
		{
			name: "confidential client with wrong secret is rejected",
			form: url.Values{
				"client_id": {"confidential-device-client"}, "client_secret": {"wrong-secret"}, "scope": {"openid"},
			},
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid_client",
		},
		{
			name: "confidential client with correct secret succeeds",
			form: url.Values{
				"client_id": {"confidential-device-client"}, "client_secret": {correctSecret}, "scope": {"openid"},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "public client succeeds without secret",
			form:       url.Values{"client_id": {"public-device-client"}, "scope": {"openid"}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown client is rejected",
			form:       url.Values{"client_id": {"no-such-client"}, "scope": {"openid"}},
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid_client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/oauth/device_authorization", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()

			h.DeviceAuthorizationHandler(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, "body: %s", rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, body["error"])
			} else {
				assert.NotEmpty(t, body["device_code"])
				assert.NotEmpty(t, body["user_code"])
			}
		})
	}
}
