// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
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
				assert.Equal(t, oauthproto.GrantTypeTokenExchange, data.Get("grant_type"))
				assert.Equal(t, testSubjectToken, data.Get("subject_token"))
				assert.Equal(t, oauthproto.TokenTypeAccessToken, data.Get("subject_token_type"))
				assert.Equal(t, oauthproto.TokenTypeAccessToken, data.Get("requested_token_type"))
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
				SubjectTokenType: oauthproto.TokenTypeIDToken,
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, oauthproto.TokenTypeIDToken, data.Get("subject_token_type"))
			},
		},
		{
			name: "with resource",
			config: &ExchangeConfig{
				TokenURL: "https://example.com/token",
				Resource: "https://api.example.com/resource",
			},
			subjectToken: testSubjectToken,
			checkForm: func(t *testing.T, data url.Values) {
				t.Helper()
				assert.Equal(t, "https://api.example.com/resource", data.Get("resource"))
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
		resp        *oauthproto.TokenResponse
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path with issued_token_type",
			resp: &oauthproto.TokenResponse{
				IssuedTokenType: oauthproto.TokenTypeAccessToken,
			},
			wantErr: false,
		},
		{
			name: "empty issued_token_type returns error",
			resp: &oauthproto.TokenResponse{
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

// TestDefaultHandler_Exists verifies that the package-level defaultHandler is
// initialized as an rfc8693Handler. It is exercised by the integration done in
// later commits; this test ensures the package symbol stays linked.
func TestDefaultHandler_Exists(t *testing.T) {
	t.Parallel()

	require.NotNil(t, defaultHandler)
	_, ok := defaultHandler.(*rfc8693Handler)
	assert.True(t, ok, "defaultHandler should be a *rfc8693Handler")
}
