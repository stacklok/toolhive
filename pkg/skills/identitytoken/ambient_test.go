// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAmbient is not run in parallel: it uses t.Setenv, which forbids it.
func TestAmbient(t *testing.T) {
	tests := []struct {
		name        string
		reqURLSet   bool
		reqTokenSet bool
		handler     http.HandlerFunc
		wantOK      bool
		wantErr     string
		wantToken   string
	}{
		{
			name:      "no request URL: unavailable, not an error",
			reqURLSet: false,
		},
		{
			name:        "no request token: unavailable, not an error",
			reqURLSet:   true,
			reqTokenSet: false,
		},
		{
			name:        "success merges audience into existing query",
			reqURLSet:   true,
			reqTokenSet: true,
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "sigstore", r.URL.Query().Get("audience"))
				assert.Equal(t, "2.0", r.URL.Query().Get("api-version"))
				assert.Equal(t, "Bearer test-request-token", r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(map[string]string{"value": "the.jwt.token"})
			},
			wantOK:    true,
			wantToken: "the.jwt.token",
		},
		{
			name:        "non-2xx status is an error",
			reqURLSet:   true,
			reqTokenSet: true,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			wantErr: "status 403",
		},
		{
			name:        "malformed body is an error",
			reqURLSet:   true,
			reqTokenSet: true,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
			wantErr: "decoding ambient OIDC token response",
		},
		{
			name:        "empty value is an error",
			reqURLSet:   true,
			reqTokenSet: true,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"value": ""})
			},
			wantErr: "empty value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.handler != nil {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				u, err := url.Parse(srv.URL)
				require.NoError(t, err)
				q := u.Query()
				q.Set("api-version", "2.0")
				u.RawQuery = q.Encode()
				t.Setenv(envRequestURL, u.String())
			} else if tc.reqURLSet {
				t.Setenv(envRequestURL, "https://example.test/token")
			} else {
				t.Setenv(envRequestURL, "")
			}
			if tc.reqTokenSet {
				t.Setenv(envRequestToken, "test-request-token")
			} else {
				t.Setenv(envRequestToken, "")
			}

			token, ok, err := Ambient(t.Context())
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.False(t, ok)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantToken, token)
			}
		})
	}
}
