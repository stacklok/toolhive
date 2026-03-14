// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEntraOBOHandler_ResolveTokenURL tests the EntraOBOHandler ResolveTokenURL
// method including tenant ID validation and security checks.
func TestEntraOBOHandler_ResolveTokenURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *ExchangeConfig
		wantURL     string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid GUID tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "550e8400-e29b-41d4-a716-446655440000",
					},
				},
			},
			wantURL: "https://login.microsoftonline.com/550e8400-e29b-41d4-a716-446655440000/oauth2/v2.0/token",
		},
		{
			name: "valid domain tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "contoso.com",
					},
				},
			},
			wantURL: "https://login.microsoftonline.com/contoso.com/oauth2/v2.0/token",
		},
		{
			name:        "nil config",
			config:      nil,
			wantErr:     true,
			errContains: "config must not be nil",
		},
		{
			name: "nil RawConfig",
			config: &ExchangeConfig{
				RawConfig: nil,
			},
			wantErr:     true,
			errContains: "entra variant requires RawConfig",
		},
		{
			name: "missing tenantId key",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{},
				},
			},
			wantErr:     true,
			errContains: "non-empty tenantId",
		},
		{
			name: "empty tenantId value",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "",
					},
				},
			},
			wantErr:     true,
			errContains: "non-empty tenantId",
		},
		{
			name: "path traversal in tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "../evil.com/",
					},
				},
			},
			wantErr:     true,
			errContains: "not a valid GUID or domain name",
		},
		{
			name: "special chars in tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "tenant/../../steal",
					},
				},
			},
			wantErr:     true,
			errContains: "not a valid GUID or domain name",
		},
		{
			name: "query injection in tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "x?foo=bar",
					},
				},
			},
			wantErr:     true,
			errContains: "not a valid GUID or domain name",
		},
		{
			name: "URL-encoded path traversal in tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "%2e%2e%2fevil.com",
					},
				},
			},
			wantErr:     true,
			errContains: "not a valid GUID or domain name",
		},
		{
			name: "null byte injection in tenantId",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "550e8400-e29b-41d4-a716-446655440000\x00.evil.com",
					},
				},
			},
			wantErr:     true,
			errContains: "not a valid GUID or domain name",
		},
		{
			name: "overlong tenantId rejected",
			config: &ExchangeConfig{
				RawConfig: &RawExchangeConfig{
					Parameters: map[string]string{
						"tenantId": "a." + string(make([]byte, 253)),
					},
				},
			},
			wantErr:     true,
			errContains: "tenantId exceeds maximum length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &EntraOBOHandler{}
			got, err := h.ResolveTokenURL(tt.config)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, got)
		})
	}
}

// TestEntraOBOHandler_BuildFormData tests the EntraOBOHandler BuildFormData
// method including validation of required fields.
func TestEntraOBOHandler_BuildFormData(t *testing.T) {
	t.Parallel()

	// Sentinel values for credential non-leakage assertions below.
	const (
		secretClientSecret = "super-secret-value-12345"
		secretSubjectToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload"
	)

	tests := []struct {
		name         string
		config       *ExchangeConfig
		subjectToken string
		wantErr      bool
		errContains  string
		checkForm    func(t *testing.T, data url.Values)
	}{
		{
			name: "happy path with all fields",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
				Scopes:       []string{"https://graph.microsoft.com/.default"},
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, grantTypeJWTBearer, data.Get("grant_type"))
				assert.Equal(t, testSubjectToken, data.Get("assertion"))
				assert.Equal(t, "on_behalf_of", data.Get("requested_token_use"))
				assert.Equal(t, "my-client-id", data.Get("client_id"))
				assert.Equal(t, "my-client-secret", data.Get("client_secret"))
				assert.Equal(t, "https://graph.microsoft.com/.default", data.Get("scope"))
			},
		},
		{
			name: "without scopes omits scope field",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
				Scopes:       nil,
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Empty(t, data.Get("scope"))
				// Ensure the key is not present at all.
				_, hasScope := data["scope"]
				assert.False(t, hasScope, "scope key should not be present when no scopes configured")
			},
		},
		{
			name:         "nil config returns error",
			config:       nil,
			subjectToken: testSubjectToken,
			wantErr:      true,
			errContains:  "config must not be nil",
		},
		{
			name: "empty subjectToken returns error",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: secretClientSecret,
			},
			subjectToken: "",
			wantErr:      true,
			errContains:  "subject_token is required",
		},
		{
			name: "empty ClientID returns error",
			config: &ExchangeConfig{
				ClientID:     "",
				ClientSecret: secretClientSecret,
			},
			subjectToken: secretSubjectToken,
			wantErr:      true,
			errContains:  "client_id is required",
		},
		{
			name: "empty ClientSecret returns error",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: "",
			},
			subjectToken: secretSubjectToken,
			wantErr:      true,
			errContains:  "client_secret is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &EntraOBOHandler{}
			data, err := h.BuildFormData(tt.config, tt.subjectToken)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				// Credentials must never leak into error messages.
				assert.NotContains(t, err.Error(), secretClientSecret,
					"error message must not contain client_secret")
				assert.NotContains(t, err.Error(), secretSubjectToken,
					"error message must not contain subject_token")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, data)
			if tt.checkForm != nil {
				tt.checkForm(t, data)
			}
		})
	}
}

// TestEntraOBOHandler_ValidateResponse tests the EntraOBOHandler ValidateResponse method.
func TestEntraOBOHandler_ValidateResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		resp        *Response
		wantErr     bool
		errContains string
	}{
		{
			name: "valid response without issued_token_type",
			resp: &Response{
				AccessToken: "tok",
				TokenType:   "Bearer",
			},
			wantErr: false,
		},
		{
			name:        "nil response returns error",
			resp:        nil,
			wantErr:     true,
			errContains: "response must not be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &EntraOBOHandler{}
			err := h.ValidateResponse(tt.resp)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestEntraOBOHandler_ClientAuth verifies that EntraOBOHandler returns empty
// clientAuthentication regardless of config credentials, because Entra OBO
// sends credentials as form parameters, not HTTP Basic Auth.
func TestEntraOBOHandler_ClientAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *ExchangeConfig
	}{
		{
			name: "returns empty even with credentials in config",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
			},
		},
		{
			name:   "returns empty for empty config",
			config: &ExchangeConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &EntraOBOHandler{}
			got := h.ClientAuth(tt.config)
			assert.Equal(t, clientAuthentication{}, got)
		})
	}
}

// TestEntraOBOHandler_Registration verifies that the init() function in
// entra_obo.go registered the EntraOBOHandler in the DefaultVariantRegistry.
func TestEntraOBOHandler_Registration(t *testing.T) {
	t.Parallel()

	handler, ok := DefaultVariantRegistry.Get("entra")
	require.True(t, ok, "entra variant should be registered in DefaultVariantRegistry")
	assert.NotNil(t, handler)
	assert.IsType(t, &EntraOBOHandler{}, handler)
}
