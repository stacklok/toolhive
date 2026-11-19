// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entra_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
)

func TestNew_ValidConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  entra.Config
	}{
		{
			name: "domain tenant with default authority",
			cfg:  entra.Config{TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret},
		},
		{
			name: "GUID tenant",
			cfg: entra.Config{
				TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47", ClientID: testClientID, ClientSecret: testClientSecret,
			},
		},
		{
			name: "explicit https authority",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "https://login.microsoftonline.us",
			},
		},
		{
			name: "loopback http authority permitted for tests",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "http://127.0.0.1:8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex, err := entra.New(tt.cfg)
			require.NoError(t, err)
			assert.NotNil(t, ex)
		})
	}
}

func TestNew_InvalidConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  entra.Config
	}{
		{
			name: "missing tenant",
			cfg:  entra.Config{ClientID: testClientID, ClientSecret: testClientSecret},
		},
		{
			name: "missing client id",
			cfg:  entra.Config{TenantID: testTenant, ClientSecret: testClientSecret},
		},
		{
			name: "missing client secret",
			cfg:  entra.Config{TenantID: testTenant, ClientID: testClientID},
		},
		{
			name: "tenant with path separator",
			cfg:  entra.Config{TenantID: "tenant/../evil", ClientID: testClientID, ClientSecret: testClientSecret},
		},
		{
			name: "tenant that is neither GUID nor domain",
			cfg:  entra.Config{TenantID: "not a real tenant", ClientID: testClientID, ClientSecret: testClientSecret},
		},
		{
			name: "single-label tenant",
			cfg:  entra.Config{TenantID: "contoso", ClientID: testClientID, ClientSecret: testClientSecret},
		},
		{
			name: "tenant longer than the DNS limit",
			cfg: entra.Config{
				TenantID: strings.Repeat("a", 254), ClientID: testClientID, ClientSecret: testClientSecret,
			},
		},
		{
			name: "non-https authority scheme",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "ftp://login.microsoftonline.com",
			},
		},
		{
			name: "non-loopback http authority",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "http://login.microsoftonline.com",
			},
		},
		{
			name: "authority with fragment",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "https://login.microsoftonline.com/#frag",
			},
		},
		{
			name: "authority with query string",
			cfg: entra.Config{
				TenantID: testTenant, ClientID: testClientID, ClientSecret: testClientSecret,
				Authority: "https://login.microsoftonline.com/?foo=bar",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex, err := entra.New(tt.cfg)
			require.Error(t, err)
			assert.Nil(t, ex)
			// Validation errors never echo the client secret.
			assert.NotContains(t, err.Error(), testClientSecret)
		})
	}
}

func TestNewFromTokenURL_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tokenURL string
	}{
		{name: "https token endpoint", tokenURL: "https://login.microsoftonline.com/test-tenant/oauth2/v2.0/token"},
		{name: "sovereign cloud https", tokenURL: "https://login.microsoftonline.us/test-tenant/oauth2/v2.0/token"},
		{name: "loopback http permitted for tests", tokenURL: "http://127.0.0.1:8080/test-tenant/oauth2/v2.0/token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex, err := entra.NewFromTokenURL(tt.tokenURL, testClientID, testClientSecret, nil)
			require.NoError(t, err)
			assert.NotNil(t, ex)
		})
	}
}

func TestNewFromTokenURL_Invalid(t *testing.T) {
	t.Parallel()

	const validURL = "https://login.microsoftonline.com/test-tenant/oauth2/v2.0/token"

	tests := []struct {
		name         string
		tokenURL     string
		clientID     string
		clientSecret string
	}{
		{name: "empty token url", tokenURL: "", clientID: testClientID, clientSecret: testClientSecret},
		{name: "non-https scheme", tokenURL: "ftp://login.microsoftonline.com/t/token", clientID: testClientID, clientSecret: testClientSecret},
		{name: "non-loopback http", tokenURL: "http://login.microsoftonline.com/t/token", clientID: testClientID, clientSecret: testClientSecret},
		{name: "token url with query", tokenURL: validURL + "?foo=bar", clientID: testClientID, clientSecret: testClientSecret},
		{name: "token url with fragment", tokenURL: validURL + "#frag", clientID: testClientID, clientSecret: testClientSecret},
		{name: "missing host", tokenURL: "https:///t/token", clientID: testClientID, clientSecret: testClientSecret},
		{name: "missing client id", tokenURL: validURL, clientID: "", clientSecret: testClientSecret},
		{name: "missing client secret", tokenURL: validURL, clientID: testClientID, clientSecret: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex, err := entra.NewFromTokenURL(tt.tokenURL, tt.clientID, tt.clientSecret, nil)
			require.Error(t, err)
			assert.Nil(t, ex)
			// Validation errors never echo the client secret.
			assert.NotContains(t, err.Error(), testClientSecret)
		})
	}
}
