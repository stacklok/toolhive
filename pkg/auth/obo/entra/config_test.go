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

func TestTokenURL_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tenantID  string
		authority string
		want      string
	}{
		{
			name:     "domain tenant with default authority",
			tenantID: testTenant,
			want:     "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token",
		},
		{
			name:     "GUID tenant",
			tenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47",
			want:     "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/oauth2/v2.0/token",
		},
		{
			name:     "tenant is trimmed",
			tenantID: "  " + testTenant + " ",
			want:     "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token",
		},
		{
			name:      "explicit https authority",
			tenantID:  testTenant,
			authority: "https://login.microsoftonline.us",
			want:      "https://login.microsoftonline.us/contoso.onmicrosoft.com/oauth2/v2.0/token",
		},
		{
			name:      "loopback http authority permitted for tests",
			tenantID:  testTenant,
			authority: "http://127.0.0.1:8080",
			want:      "http://127.0.0.1:8080/contoso.onmicrosoft.com/oauth2/v2.0/token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := entra.TokenURL(tt.tenantID, tt.authority)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			// Every URL TokenURL derives must be accepted by NewFromTokenURL.
			ex, err := entra.NewFromTokenURL(got, testClientID, testClientSecret, nil)
			require.NoError(t, err)
			assert.NotNil(t, ex)
		})
	}
}

func TestTokenURL_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tenantID  string
		authority string
		wantErr   string
	}{
		{name: "missing tenant", tenantID: "", wantErr: "TenantID is required"},
		{name: "whitespace tenant", tenantID: "   ", wantErr: "TenantID is required"},
		{name: "tenant with path separator", tenantID: "tenant/../evil", wantErr: "GUID or a domain"},
		{name: "tenant that is neither GUID nor domain", tenantID: "not a real tenant", wantErr: "GUID or a domain"},
		{name: "single-label tenant", tenantID: "contoso", wantErr: "GUID or a domain"},
		{name: "tenant longer than the DNS limit", tenantID: strings.Repeat("a", 254), wantErr: "at most 253"},
		{
			name: "non-https authority scheme", tenantID: testTenant,
			authority: "ftp://login.microsoftonline.com", wantErr: "Authority scheme must be https",
		},
		{
			name: "non-loopback http authority", tenantID: testTenant,
			authority: "http://login.microsoftonline.com", wantErr: "http allowed only for loopback",
		},
		{
			name: "authority with fragment", tenantID: testTenant,
			authority: "https://login.microsoftonline.com/#frag", wantErr: "must not contain a fragment",
		},
		{
			name: "authority with query string", tenantID: testTenant,
			authority: "https://login.microsoftonline.com/?foo=bar", wantErr: "must not contain a query string",
		},
		{
			name: "authority without host", tenantID: testTenant,
			authority: "https:///", wantErr: "Authority must include a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := entra.TokenURL(tt.tenantID, tt.authority)
			require.ErrorContains(t, err, tt.wantErr)
			assert.Empty(t, got)
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
