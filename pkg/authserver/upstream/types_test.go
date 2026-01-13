// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstream

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokens_IsExpired(t *testing.T) {
	t.Parallel()

	t.Run("nil tokens returns true (treated as expired)", func(t *testing.T) {
		t.Parallel()
		var tokens *Tokens
		assert.True(t, tokens.IsExpired())
	})

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "token already expired",
			expiresAt: time.Now().Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "token expires within buffer period",
			expiresAt: time.Now().Add(15 * time.Second),
			want:      true,
		},
		{
			name:      "token expires exactly at buffer boundary",
			expiresAt: time.Now().Add(tokenExpirationBuffer),
			want:      true,
		},
		{
			name:      "token expires just after buffer period",
			expiresAt: time.Now().Add(tokenExpirationBuffer + 1*time.Second),
			want:      false,
		},
		{
			name:      "token expires well in the future",
			expiresAt: time.Now().Add(1 * time.Hour),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := &Tokens{
				AccessToken: "test-token",
				ExpiresAt:   tt.expiresAt,
			}
			got := tokens.IsExpired()
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_validateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		wantErr     bool
		errContains string
	}{
		// Valid URIs
		{
			name:    "valid HTTPS URI with path",
			uri:     "https://auth.example.com/oauth/callback",
			wantErr: false,
		},
		{
			name:    "valid HTTPS URI with port",
			uri:     "https://auth.example.com:8443/oauth/callback",
			wantErr: false,
		},
		{
			name:    "valid HTTP URI with loopback IPv4",
			uri:     "http://127.0.0.1:8080/oauth/callback",
			wantErr: false,
		},
		{
			name:    "valid HTTP URI with loopback IPv6",
			uri:     "http://[::1]:8080/oauth/callback",
			wantErr: false,
		},
		{
			name:    "valid HTTP URI with localhost",
			uri:     "http://localhost:8080/oauth/callback",
			wantErr: false,
		},
		{
			name:    "valid HTTPS URI without path",
			uri:     "https://example.com",
			wantErr: false,
		},

		// Invalid URIs
		{
			name:        "HTTP to non-loopback address",
			uri:         "http://example.com/callback",
			wantErr:     true,
			errContains: "redirect_uri with http scheme requires loopback address (127.0.0.1, ::1, or localhost)",
		},
		{
			name:        "URI contains fragment",
			uri:         "https://example.com/callback#section",
			wantErr:     true,
			errContains: "redirect_uri must not contain a fragment (#)",
		},
		{
			name:        "URI contains userinfo",
			uri:         "https://user:pass@example.com/callback",
			wantErr:     true,
			errContains: "redirect_uri must not contain user credentials",
		},
		{
			name:        "invalid scheme (ftp)",
			uri:         "ftp://example.com/callback",
			wantErr:     true,
			errContains: "redirect_uri must use http or https scheme",
		},
		{
			name:        "relative URI",
			uri:         "/oauth/callback",
			wantErr:     true,
			errContains: "redirect_uri must be an absolute URL with scheme and host",
		},
		{
			name:        "wildcard hostname",
			uri:         "https://*/callback",
			wantErr:     true,
			errContains: "redirect_uri must not contain wildcard hostname",
		},
		{
			name:        "empty URI",
			uri:         "",
			wantErr:     true,
			errContains: "redirect_uri must be an absolute URL with scheme and host",
		},
		{
			name:        "scheme only",
			uri:         "https://",
			wantErr:     true,
			errContains: "redirect_uri must be an absolute URL with scheme and host",
		},
		{
			name:        "wildcard subdomain",
			uri:         "https://*.example.com/callback",
			wantErr:     true,
			errContains: "redirect_uri must not contain wildcard hostname",
		},
		{
			name:        "malformed URL with invalid percent encoding",
			uri:         "https://example.com/path%ZZ",
			wantErr:     true,
			errContains: "redirect_uri must be an absolute URL with scheme and host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRedirectURI(tt.uri)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func Test_validateRedirectURI_LoopbackAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uri     string
		wantErr bool
	}{
		// Loopback addresses with HTTP should be allowed
		{
			name:    "HTTP with localhost",
			uri:     "http://localhost/callback",
			wantErr: false,
		},
		{
			name:    "HTTP with localhost and port",
			uri:     "http://localhost:8080/callback",
			wantErr: false,
		},
		{
			name:    "HTTP with 127.0.0.1",
			uri:     "http://127.0.0.1/callback",
			wantErr: false,
		},
		{
			name:    "HTTP with 127.0.0.1 and port",
			uri:     "http://127.0.0.1:8080/callback",
			wantErr: false,
		},
		{
			name:    "HTTP with IPv6 ::1",
			uri:     "http://[::1]/callback",
			wantErr: false,
		},
		{
			name:    "HTTP with IPv6 ::1 and port",
			uri:     "http://[::1]:8080/callback",
			wantErr: false,
		},
		// Non-loopback addresses with HTTP should be rejected
		{
			name:    "HTTP with non-loopback hostname",
			uri:     "http://example.com/callback",
			wantErr: true,
		},
		{
			name:    "HTTP with non-loopback hostname and port",
			uri:     "http://example.com:8080/callback",
			wantErr: true,
		},
		{
			name:    "HTTP with non-loopback IP",
			uri:     "http://192.168.1.1/callback",
			wantErr: true,
		},
		{
			name:    "HTTP with non-loopback IP and port",
			uri:     "http://192.168.1.1:8080/callback",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRedirectURI(tt.uri)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "redirect_uri with http scheme requires loopback address")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

