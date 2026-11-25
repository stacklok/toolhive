// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwtbearer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/oauthtest"
)

const (
	testAssertion    = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test-assertion-payload.signature"
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret" //nolint:gosec // G101: test value, not a real credential
)

// baseConfig returns a Config wired to serverURL with the standard test
// client credentials and a static assertion provider. Subtests override only
// the fields that vary.
func baseConfig(serverURL string) *Config {
	return &Config{
		TokenURL:          serverURL,
		ClientID:          testClientID,
		ClientSecret:      testClientSecret,
		AssertionProvider: func() (string, error) { return testAssertion, nil },
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name: "valid config with all fields",
			config: Config{
				TokenURL:          "https://as.example.com/token",
				ClientID:          testClientID,
				ClientSecret:      testClientSecret,
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
		},
		{
			name: "valid config without client credentials",
			config: Config{
				TokenURL:          "https://as.example.com/token",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
		},
		{
			name: "missing TokenURL",
			config: Config{
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "TokenURL is required",
		},
		{
			name: "missing AssertionProvider",
			config: Config{
				TokenURL: "https://as.example.com/token",
			},
			wantErr: "AssertionProvider is required",
		},
		{
			name: "http on localhost is permitted",
			config: Config{
				TokenURL:          "http://localhost:8080/token",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
		},
		{
			name: "http on non-localhost is rejected",
			config: Config{
				TokenURL:          "http://as.example.com/token",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must use HTTPS",
		},
		{
			name: "non-http scheme is rejected",
			config: Config{
				TokenURL:          "file:///etc/passwd",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must use HTTPS",
		},
		{
			name: "non-http scheme is rejected even with InsecureAllowHTTP",
			config: Config{
				TokenURL:          "ftp://as.example.com/token",
				InsecureAllowHTTP: true,
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must use http or https scheme",
		},
		{
			name: "fragment is rejected",
			config: Config{
				TokenURL:          "https://as.example.com/token#section",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must not contain a fragment",
		},
		{
			name: "missing host is rejected",
			config: Config{
				TokenURL:          "https:///token",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must include a host",
		},
		{
			name: "embedded credentials are rejected",
			config: Config{
				TokenURL:          "https://user:pass@as.example.com/token",
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			wantErr: "must not contain embedded credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestConfig_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           *Config
		expectedContains []string
		expectedExcludes []string
	}{
		{
			name: "redacts client secret",
			config: &Config{
				TokenURL:          "https://as.example.com/token",
				ClientID:          testClientID,
				ClientSecret:      testClientSecret,
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			expectedContains: []string{"[REDACTED]"},
			expectedExcludes: []string{testClientSecret},
		},
		{
			name: "shows empty for missing secret",
			config: &Config{
				TokenURL:          "https://as.example.com/token",
				ClientID:          testClientID,
				AssertionProvider: func() (string, error) { return testAssertion, nil },
			},
			expectedContains: []string{"<empty>"},
		},
		{
			name: "shows empty for nil assertion provider",
			config: &Config{
				TokenURL: "https://as.example.com/token",
			},
			expectedContains: []string{"Assertion: <empty>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := tt.config.String()
			for _, want := range tt.expectedContains {
				assert.Contains(t, s, want)
			}
			for _, forbidden := range tt.expectedExcludes {
				assert.NotContains(t, s, forbidden)
			}
		})
	}
}

func TestTokenSource_Token_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		scopes          []string
		expectedScope   string
		responseBuilder func() []byte
		wantToken       string
		wantExpiresIn   int
	}{
		{
			name:          "without scopes",
			scopes:        nil,
			expectedScope: "",
			responseBuilder: func() []byte {
				return oauthtest.NewResponse().
					WithAccessToken("target-access-token").
					WithExpiresIn(7200).
					Build()
			},
			wantToken:     "target-access-token",
			wantExpiresIn: 7200,
		},
		{
			name:          "with scopes joined by space",
			scopes:        []string{"todos.read", "todos.write"},
			expectedScope: "todos.read todos.write",
			responseBuilder: func() []byte {
				return oauthtest.NewResponse().
					WithAccessToken("scoped-token").
					WithExpiresIn(3600).
					WithScope("todos.read todos.write").
					Build()
			},
			wantToken:     "scoped-token",
			wantExpiresIn: 3600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

				username, password, ok := r.BasicAuth()
				require.True(t, ok, "Basic Auth credentials should be present")
				assert.Equal(t, testClientID, username)
				assert.Equal(t, testClientSecret, password)

				require.NoError(t, r.ParseForm())
				assert.Equal(t, oauthproto.GrantTypeJWTBearer, r.Form.Get("grant_type"))
				assert.Equal(t, testAssertion, r.Form.Get("assertion"))
				assert.Equal(t, tt.expectedScope, r.Form.Get("scope"))

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(tt.responseBuilder())
			}))
			t.Cleanup(server.Close)

			config := baseConfig(server.URL)
			config.Scopes = tt.scopes

			token, err := config.TokenSource(context.Background()).Token()

			require.NoError(t, err)
			assert.Equal(t, tt.wantToken, token.AccessToken)
			assert.Equal(t, "Bearer", token.TokenType)
			assert.WithinDuration(t, time.Now().Add(time.Duration(tt.wantExpiresIn)*time.Second), token.Expiry, 5*time.Second)
		})
	}
}

func TestTokenSource_Token_Errors(t *testing.T) {
	t.Parallel()

	t.Run("assertion provider returns error", func(t *testing.T) {
		t.Parallel()

		config := baseConfig("https://as.example.com/token")
		config.AssertionProvider = func() (string, error) {
			return "", fmt.Errorf("assertion unavailable")
		}

		_, err := config.TokenSource(context.Background()).Token()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get assertion")
		assert.Contains(t, err.Error(), "assertion unavailable")
	})

	t.Run("server returns 400 with OAuth error, body scrubbed", func(t *testing.T) {
		t.Parallel()

		rawBody := oauthtest.NewErrorResponse().
			WithError("invalid_grant").
			WithDescription("assertion expired").
			WithURI("https://example.com/errors/invalid_grant").
			Build()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(rawBody)
		}))
		t.Cleanup(server.Close)

		_, err := baseConfig(server.URL).TokenSource(context.Background()).Token()

		require.Error(t, err)

		var re *oauth2.RetrieveError
		require.True(t, errors.As(err, &re), "error must unwrap to *oauth2.RetrieveError, got %T: %v", err, err)
		assert.Equal(t, "invalid_grant", re.ErrorCode)
		assert.Equal(t, "assertion expired", re.ErrorDescription)
		assert.Equal(t, "https://example.com/errors/invalid_grant", re.ErrorURI)
		require.NotNil(t, re.Response)
		assert.Equal(t, http.StatusBadRequest, re.Response.StatusCode)
		// Body is scrubbed to keep raw upstream content out of error strings.
		assert.Nil(t, re.Body, "RetrieveError.Body should be scrubbed")
	})

	t.Run("empty token_type in response is rejected (RFC 6749 §5.1)", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			body := oauthtest.NewResponse().
				WithAccessToken("target-access-token").
				WithTokenType("").
				Build()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		}))
		t.Cleanup(server.Close)

		_, err := baseConfig(server.URL).TokenSource(context.Background()).Token()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty token_type")
		assert.Contains(t, err.Error(), "jwtbearer:")
	})
}
