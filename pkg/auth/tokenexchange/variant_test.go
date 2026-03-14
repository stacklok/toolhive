// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVariantRegistry tests the VariantRegistry Register and Get methods
// including case normalization behavior.
func TestVariantRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		registerName string
		getName      string
		wantFound    bool
	}{
		{
			name:         "register and get round-trip",
			registerName: "myvariant",
			getName:      "myvariant",
			wantFound:    true,
		},
		{
			name:         "get miss returns nil and false",
			registerName: "registered",
			getName:      "nonexistent",
			wantFound:    false,
		},
		{
			name:         "register uppercase get lowercase",
			registerName: "ENTRA",
			getName:      "entra",
			wantFound:    true,
		},
		{
			name:         "register lowercase get mixed case",
			registerName: "entra",
			getName:      "Entra",
			wantFound:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := NewVariantRegistry()
			reg.Register(tt.registerName, &rfc8693Handler{})

			handler, ok := reg.Get(tt.getName)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.NotNil(t, handler)
			} else {
				assert.Nil(t, handler)
			}
		})
	}
}

// TestVariantRegistry_PanicOnEmptyName verifies that Register panics when
// given an empty variant name.
func TestVariantRegistry_PanicOnEmptyName(t *testing.T) {
	t.Parallel()

	reg := NewVariantRegistry()
	assert.PanicsWithValue(t,
		"tokenexchange: Register variant name must not be empty",
		func() { reg.Register("", &rfc8693Handler{}) },
	)
}

// TestVariantRegistry_PanicOnNilHandler verifies that Register panics when
// given a nil handler.
func TestVariantRegistry_PanicOnNilHandler(t *testing.T) {
	t.Parallel()

	reg := NewVariantRegistry()
	assert.PanicsWithValue(t,
		"tokenexchange: Register handler must not be nil",
		func() { reg.Register("test", nil) },
	)
}

// TestVariantRegistry_PanicOnDuplicate verifies that Register panics when
// a handler is already registered under the same name.
func TestVariantRegistry_PanicOnDuplicate(t *testing.T) {
	t.Parallel()

	reg := NewVariantRegistry()
	reg.Register("dup", &rfc8693Handler{})
	assert.PanicsWithValue(t,
		`tokenexchange: duplicate variant registration: "dup"`,
		func() { reg.Register("dup", &rfc8693Handler{}) },
	)
}

// TestRFC8693Handler_ResolveTokenURL tests the rfc8693Handler ResolveTokenURL method.
func TestRFC8693Handler_ResolveTokenURL(t *testing.T) {
	t.Parallel()

	t.Run("returns empty string for valid config", func(t *testing.T) {
		t.Parallel()

		h := &rfc8693Handler{}
		got, err := h.ResolveTokenURL(&ExchangeConfig{TokenURL: "https://example.com/token"})
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("returns error for nil config", func(t *testing.T) {
		t.Parallel()

		h := &rfc8693Handler{}
		_, err := h.ResolveTokenURL(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config must not be nil")
	})
}

// TestRFC8693Handler_BuildFormData tests the rfc8693Handler BuildFormData method.
func TestRFC8693Handler_BuildFormData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *ExchangeConfig
		subjectToken string
		wantErr      bool
		errContains  string
		checkForm    func(t *testing.T, data url.Values)
	}{
		{
			name: "happy path with correct fields",
			config: &ExchangeConfig{
				TokenURL: "https://example.com/token",
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, grantTypeTokenExchange, data.Get("grant_type"))
				assert.Equal(t, testSubjectToken, data.Get("subject_token"))
				assert.Equal(t, tokenTypeAccessToken, data.Get("subject_token_type"))
				assert.Equal(t, tokenTypeAccessToken, data.Get("requested_token_type"))
			},
		},
		{
			name: "with audience and scopes",
			config: &ExchangeConfig{
				TokenURL: "https://example.com/token",
				Audience: "https://api.example.com",
				Scopes:   []string{"read", "write"},
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, "https://api.example.com", data.Get("audience"))
				assert.Equal(t, "read write", data.Get("scope"))
			},
		},
		{
			name: "with custom SubjectTokenType",
			config: &ExchangeConfig{
				TokenURL:         "https://example.com/token",
				SubjectTokenType: tokenTypeIDToken,
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, tokenTypeIDToken, data.Get("subject_token_type"))
			},
		},
		{
			name: "empty subjectToken returns error",
			config: &ExchangeConfig{
				TokenURL: "https://example.com/token",
			},
			subjectToken: "",
			wantErr:      true,
			errContains:  "subject_token is required",
		},
		{
			name:         "nil config returns error",
			config:       nil,
			subjectToken: testSubjectToken,
			wantErr:      true,
			errContains:  "config must not be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &rfc8693Handler{}
			data, err := h.BuildFormData(tt.config, tt.subjectToken)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
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

// TestRFC8693Handler_ValidateResponse tests the rfc8693Handler ValidateResponse method.
func TestRFC8693Handler_ValidateResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		resp        *Response
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path with issued_token_type",
			resp: &Response{
				AccessToken:     "tok",
				TokenType:       "Bearer",
				IssuedTokenType: tokenTypeAccessToken,
			},
			wantErr: false,
		},
		{
			name: "empty issued_token_type returns error",
			resp: &Response{
				AccessToken:     "tok",
				TokenType:       "Bearer",
				IssuedTokenType: "",
			},
			wantErr:     true,
			errContains: "empty issued_token_type",
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

			h := &rfc8693Handler{}
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

// TestRFC8693Handler_ClientAuth tests the rfc8693Handler ClientAuth method.
func TestRFC8693Handler_ClientAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *ExchangeConfig
		want   clientAuthentication
	}{
		{
			name: "returns populated credentials from config",
			config: &ExchangeConfig{
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
			},
			want: clientAuthentication{
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
			},
		},
		{
			name:   "returns empty credentials when config has none",
			config: &ExchangeConfig{},
			want:   clientAuthentication{},
		},
		{
			name:   "nil config returns empty credentials",
			config: nil,
			want:   clientAuthentication{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := &rfc8693Handler{}
			got := h.ClientAuth(tt.config)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestExchangeConfig_ResolveHandler tests the resolveHandler method on ExchangeConfig.
func TestExchangeConfig_ResolveHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      ExchangeConfig
		wantErr     bool
		errContains string
	}{
		{
			name:   "empty variant returns defaultHandler",
			config: ExchangeConfig{Variant: ""},
		},
		{
			name: "unknown variant returns error",
			config: ExchangeConfig{
				Variant:         "nosuchvariant",
				VariantRegistry: NewVariantRegistry(),
			},
			wantErr:     true,
			errContains: "unsupported token exchange variant",
		},
		{
			name: "known variant from injected registry",
			config: func() ExchangeConfig {
				reg := NewVariantRegistry()
				reg.Register("custom", &rfc8693Handler{})
				return ExchangeConfig{
					Variant:         "custom",
					VariantRegistry: reg,
				}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, err := tt.config.resolveHandler()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, handler)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, handler)
		})
	}
}

// TestTokenSource_WithVariant is an integration test that verifies the full
// token exchange flow using a registered variant handler (EntraOBOHandler).
// It uses a test HTTP server to validate the outgoing request matches the
// Entra OBO protocol.
func TestTokenSource_WithVariant(t *testing.T) {
	t.Parallel()

	const (
		wantAccessToken = "entra-obo-exchanged-token"
		testClientID    = "entra-client-id"
		testClientSec   = "entra-client-secret"
	)

	// Create a test server that validates the Entra OBO form body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// Entra OBO should NOT use HTTP Basic Auth; credentials are in form data only.
		_, _, hasBasic := r.BasicAuth()
		assert.False(t, hasBasic, "Entra OBO should not use Basic Auth")
		assert.Empty(t, r.Header.Get("Authorization"), "Entra OBO should not send Authorization header")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)

		// Verify all required Entra OBO form fields.
		assert.Equal(t, grantTypeJWTBearer, form.Get("grant_type"))
		assert.Equal(t, testSubjectToken, form.Get("assertion"))
		assert.Equal(t, "on_behalf_of", form.Get("requested_token_use"))
		assert.Equal(t, testClientID, form.Get("client_id"))
		assert.Equal(t, testClientSec, form.Get("client_secret"))
		assert.Equal(t, "https://graph.microsoft.com/.default", form.Get("scope"))

		// Return an Entra-style response (no issued_token_type).
		resp := Response{
			AccessToken: wantAccessToken,
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	// Build an isolated registry with the Entra handler.
	reg := NewVariantRegistry()
	reg.Register("entra", &EntraOBOHandler{})

	config := &ExchangeConfig{
		// Set TokenURL directly to bypass ResolveTokenURL (which builds a
		// login.microsoftonline.com URL).
		TokenURL:     server.URL,
		ClientID:     testClientID,
		ClientSecret: testClientSec,
		Scopes:       []string{"https://graph.microsoft.com/.default"},
		Variant:      "entra",
		RawConfig: &RawExchangeConfig{
			Parameters: map[string]string{
				"tenantId": "00000000-0000-0000-0000-000000000000",
			},
		},
		VariantRegistry: reg,
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	ctx := context.Background()
	ts, err := config.TokenSource(ctx)
	require.NoError(t, err)

	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, wantAccessToken, token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.False(t, token.Expiry.IsZero())
}

// TestTokenSource_WithVariant_UnknownVariant verifies that TokenSource returns
// an error when an unregistered variant is specified.
func TestTokenSource_WithVariant_UnknownVariant(t *testing.T) {
	t.Parallel()

	config := &ExchangeConfig{
		TokenURL:        "https://example.com/token",
		Variant:         "nosuch",
		VariantRegistry: NewVariantRegistry(),
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	ctx := context.Background()
	ts, err := config.TokenSource(ctx)
	require.Error(t, err)
	assert.Nil(t, ts)
	assert.Contains(t, err.Error(), "unsupported token exchange variant")
	assert.Contains(t, err.Error(), "nosuch")
}

// resolveTokenURLTestHandler is a minimal VariantHandler that returns a fixed URL
// from ResolveTokenURL. Used to test the URL resolution path in TokenSource.
type resolveTokenURLTestHandler struct {
	rfc8693Handler
	url string
}

func (h *resolveTokenURLTestHandler) ResolveTokenURL(_ *ExchangeConfig) (string, error) {
	return h.url, nil
}

// TestTokenSource_WithVariant_ResolvesTokenURL verifies that when TokenURL is
// empty, the variant handler's ResolveTokenURL is invoked and its result is used.
func TestTokenSource_WithVariant_ResolvesTokenURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := newResponse().withAccessToken("resolved-token").build()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reg := NewVariantRegistry()
	reg.Register("resolvetest", &resolveTokenURLTestHandler{url: server.URL})

	config := &ExchangeConfig{
		// TokenURL intentionally empty — the handler should resolve it.
		Variant:         "resolvetest",
		VariantRegistry: reg,
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	ctx := context.Background()
	ts, err := config.TokenSource(ctx)
	require.NoError(t, err)

	token, err := ts.Token()
	require.NoError(t, err)
	assert.Equal(t, "resolved-token", token.AccessToken)
}
