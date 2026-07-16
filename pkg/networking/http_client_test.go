// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestNewHttpClientBuilder(t *testing.T) {
	t.Parallel()

	builder := NewHttpClientBuilder()

	assert.Equal(t, HttpTimeout, builder.clientTimeout)
	assert.Equal(t, 10*time.Second, builder.tlsHandshakeTimeout)
	assert.Equal(t, 10*time.Second, builder.responseHeaderTimeout)
	assert.Empty(t, builder.caCertPath)
	assert.Empty(t, builder.authTokenFile)
	assert.False(t, builder.allowPrivate)
}

func TestNewHostScopedClientBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		host              string
		allowPrivateIPs   bool
		insecureAllowHTTP bool
		wantAllowPrivate  bool
		wantInsecureHTTP  bool
	}{
		{
			name: "external host, guarded by default",
			host: "idp.example.com",
		},
		{
			name:             "external host, allowPrivateIPs widens only the private-IP gate",
			host:             "idp.example.com",
			allowPrivateIPs:  true,
			wantAllowPrivate: true,
			wantInsecureHTTP: false,
		},
		{
			name:              "external host, insecureAllowHTTP widens both gates",
			host:              "idp.example.com",
			insecureAllowHTTP: true,
			wantAllowPrivate:  true,
			wantInsecureHTTP:  true,
		},
		{
			name:             "loopback host is exempted from both gates regardless of flags",
			host:             "127.0.0.1:8443",
			wantAllowPrivate: true,
			wantInsecureHTTP: true,
		},
		{
			name:             "loopback host stays exempted even with allowPrivateIPs also set",
			host:             "localhost",
			allowPrivateIPs:  true,
			wantAllowPrivate: true,
			wantInsecureHTTP: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewHostScopedClientBuilder(tt.host, tt.allowPrivateIPs, tt.insecureAllowHTTP)

			assert.Equal(t, tt.wantAllowPrivate, builder.allowPrivate, "allowPrivate mismatch")
			assert.Equal(t, tt.wantInsecureHTTP, builder.insecureAllowHTTP, "insecureAllowHTTP mismatch")
		})
	}
}

// TestNewHostScopedClientBuilder_InsecureDisableURLValidationEnvVar pins that
// the env var widens both gates the same way a loopback host does. Kept as a
// standalone test (not a table case) because t.Setenv is incompatible with
// t.Parallel.
func TestNewHostScopedClientBuilder_InsecureDisableURLValidationEnvVar(t *testing.T) {
	t.Setenv("INSECURE_DISABLE_URL_VALIDATION", "true")

	builder := NewHostScopedClientBuilder("idp.example.com", false, false)

	assert.True(t, builder.allowPrivate, "env var must widen the private-IP gate")
	assert.True(t, builder.insecureAllowHTTP, "env var must widen the HTTP scheme gate")
}

func TestHttpClientBuilder_WithCABundle(t *testing.T) {
	t.Parallel()

	builder := NewHttpClientBuilder()
	path := "/path/to/ca.crt"

	result := builder.WithCABundle(path)

	assert.Same(t, builder, result) // fluent interface
	assert.Equal(t, path, builder.caCertPath)
}

func TestHttpClientBuilder_WithTokenFromFile(t *testing.T) {
	t.Parallel()

	builder := NewHttpClientBuilder()
	path := "/path/to/token"

	result := builder.WithTokenFromFile(path)

	assert.Same(t, builder, result) // fluent interface
	assert.Equal(t, path, builder.authTokenFile)
}

func TestHttpClientBuilder_WithPrivateIPs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		allow bool
	}{
		{
			name:  "allow private IPs",
			allow: true,
		},
		{
			name:  "disallow private IPs",
			allow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := NewHttpClientBuilder()
			result := builder.WithPrivateIPs(tt.allow)

			assert.Same(t, builder, result) // fluent interface
			assert.Equal(t, tt.allow, builder.allowPrivate)
		})
	}
}

func TestHttpClientBuilder_Build(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupBuilder   func() *HttpClientBuilder
		setupFiles     func(t *testing.T) (string, string) // returns caCertPath, tokenPath
		expectError    bool
		errorContains  string
		validateClient func(t *testing.T, client *http.Client)
	}{
		{
			name: "basic client without options",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(_ *testing.T) (string, string) {
				return "", ""
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				assert.Equal(t, HttpTimeout, client.Timeout)
				assert.IsType(t, &ValidatingTransport{}, client.Transport)
			},
		},
		{
			name: "client with valid CA bundle",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				// Create a valid CA certificate for testing
				caCert := `-----BEGIN CERTIFICATE-----
MIIDeTCCAmGgAwIBAgIUN4MtKQdT5lEx53a3ZnUoSuAQ5fswDQYJKoZIhvcNAQEL
BQAwTDELMAkGA1UEBhMCVVMxDTALBgNVBAgMBFRlc3QxDTALBgNVBAcMBFRlc3Qx
DTALBgNVBAoMBFRlc3QxEDAOBgNVBAMMB1Rlc3QgQ0EwHhcNMjUwNzA3MTMyNzIw
WhcNMjYwNzA3MTMyNzIwWjBMMQswCQYDVQQGEwJVUzENMAsGA1UECAwEVGVzdDEN
MAsGA1UEBwwEVGVzdDENMAsGA1UECgwEVGVzdDEQMA4GA1UEAwwHVGVzdCBDQTCC
ASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAN/hmz1T3M+HSjarU4qk8oMz
sYX/PI+TMPC5rHSbQ1+Tve2EwbDKUu2d4wT60lHlcVJ3eEw4N6OuRq6DV2mgmbcY
RzJLorgqLG7WsXv660azu0Ln14kK1z+x4cAYzvQ9x54g1PPep7RNPNUEBex0AjG+
m3BZSk42t76TJg/82KxT2KmmNs6iUwXBptkaGw7CSBKGQOMq00jq0Xcp+ttfZtfx
IGZ9Q5ABc/j1FhPW96NxYbkdTJrhSbsoxWeRx8RSr5r5ZsP4IBw25t3oL8SZKNsR
Ln3Whb9GkupnAfVHxAPOTSwttLa1RqFJJwpBUQErSyD7aoisd5/pMjw0+9wk/IEC
AwEAAaNTMFEwHQYDVR0OBBYEFCl3yBkrEQ9qGGSPanmhwNqyqy7/MB8GA1UdIwQY
MBaAFCl3yBkrEQ9qGGSPanmhwNqyqy7/MA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZI
hvcNAQELBQADggEBAFpv9f+xbCjuvaaNJg1s8UtVzgiJXkMYfvD+EvN2FRHkR++0
PIpeq1khxoP/INCXFBDz2+4N7nZUi79FH+IkXVAAK9w1Vg8mFOHkiRpCvHxOMU3J
FN0qsmIyA3D8LYQwJZDi6QE9qiNKGTnk7h676rAgk+ez2NS+nJNHUrPKu5zVCU4r
SaYEYg/JrY5DzgHel85LjteLiGE+6HVf8kKXAxSmxdxTDH73jdpEBtxVYxhnnxpF
d3JSN0mL1/vDlI27PofXsisvLH29wRo4Cev+naGLtdB5D8tZ6F6WBYaa9ZK86JSJ
lT/G27CBRUlDiDhthwY1dccTCFhICg6ENUGqh2I=
-----END CERTIFICATE-----`
				tmpFile := filepath.Join(t.TempDir(), "ca.crt")
				require.NoError(t, os.WriteFile(tmpFile, []byte(caCert), 0644))
				return tmpFile, ""
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				transport := client.Transport.(*ValidatingTransport)
				httpTransport := transport.Transport.(*http.Transport)
				assert.NotNil(t, httpTransport.TLSClientConfig)
				assert.NotNil(t, httpTransport.TLSClientConfig.RootCAs)
				assert.Equal(t, uint16(tls.VersionTLS12), httpTransport.TLSClientConfig.MinVersion)
			},
		},
		{
			name: "client with valid token file",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				tokenFile := filepath.Join(t.TempDir(), "token")
				require.NoError(t, os.WriteFile(tokenFile, []byte("test-token-123"), 0644))
				return "", tokenFile
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				assert.IsType(t, &oauth2.Transport{}, client.Transport)
			},
		},
		{
			name: "client with CA bundle and token",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				caCert := `-----BEGIN CERTIFICATE-----
MIIDeTCCAmGgAwIBAgIUN4MtKQdT5lEx53a3ZnUoSuAQ5fswDQYJKoZIhvcNAQEL
BQAwTDELMAkGA1UEBhMCVVMxDTALBgNVBAgMBFRlc3QxDTALBgNVBAcMBFRlc3Qx
DTALBgNVBAoMBFRlc3QxEDAOBgNVBAMMB1Rlc3QgQ0EwHhcNMjUwNzA3MTMyNzIw
WhcNMjYwNzA3MTMyNzIwWjBMMQswCQYDVQQGEwJVUzENMAsGA1UECAwEVGVzdDEN
MAsGA1UEBwwEVGVzdDENMAsGA1UECgwEVGVzdDEQMA4GA1UEAwwHVGVzdCBDQTCC
ASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBAN/hmz1T3M+HSjarU4qk8oMz
sYX/PI+TMPC5rHSbQ1+Tve2EwbDKUu2d4wT60lHlcVJ3eEw4N6OuRq6DV2mgmbcY
RzJLorgqLG7WsXv660azu0Ln14kK1z+x4cAYzvQ9x54g1PPep7RNPNUEBex0AjG+
m3BZSk42t76TJg/82KxT2KmmNs6iUwXBptkaGw7CSBKGQOMq00jq0Xcp+ttfZtfx
IGZ9Q5ABc/j1FhPW96NxYbkdTJrhSbsoxWeRx8RSr5r5ZsP4IBw25t3oL8SZKNsR
Ln3Whb9GkupnAfVHxAPOTSwttLa1RqFJJwpBUQErSyD7aoisd5/pMjw0+9wk/IEC
AwEAAaNTMFEwHQYDVR0OBBYEFCl3yBkrEQ9qGGSPanmhwNqyqy7/MB8GA1UdIwQY
MBaAFCl3yBkrEQ9qGGSPanmhwNqyqy7/MA8GA1UdEwEB/wQFMAMBAf8wDQYJKoZI
hvcNAQELBQADggEBAFpv9f+xbCjuvaaNJg1s8UtVzgiJXkMYfvD+EvN2FRHkR++0
PIpeq1khxoP/INCXFBDz2+4N7nZUi79FH+IkXVAAK9w1Vg8mFOHkiRpCvHxOMU3J
FN0qsmIyA3D8LYQwJZDi6QE9qiNKGTnk7h676rAgk+ez2NS+nJNHUrPKu5zVCU4r
SaYEYg/JrY5DzgHel85LjteLiGE+6HVf8kKXAxSmxdxTDH73jdpEBtxVYxhnnxpF
d3JSN0mL1/vDlI27PofXsisvLH29wRo4Cev+naGLtdB5D8tZ6F6WBYaa9ZK86JSJ
lT/G27CBRUlDiDhthwY1dccTCFhICg6ENUGqh2I=
-----END CERTIFICATE-----`
				caCertFile := filepath.Join(t.TempDir(), "ca.crt")
				require.NoError(t, os.WriteFile(caCertFile, []byte(caCert), 0644))

				tokenFile := filepath.Join(t.TempDir(), "token")
				require.NoError(t, os.WriteFile(tokenFile, []byte("test-token-456"), 0644))

				return caCertFile, tokenFile
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				// Should have oauth2 transport wrapping validating transport
				authTransport := client.Transport.(*oauth2.Transport)
				assert.IsType(t, &ValidatingTransport{}, authTransport.Base)
			},
		},
		{
			name: "client with private IPs allowed",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder().WithPrivateIPs(true)
			},
			setupFiles: func(_ *testing.T) (string, string) {
				return "", ""
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				transport := client.Transport.(*ValidatingTransport)
				httpTransport := transport.Transport.(*http.Transport)
				assert.Nil(t, httpTransport.DialContext)
			},
		},
		{
			name: "client with private IPs disallowed",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder().WithPrivateIPs(false)
			},
			setupFiles: func(_ *testing.T) (string, string) {
				return "", ""
			},
			expectError: false,
			validateClient: func(t *testing.T, client *http.Client) {
				t.Helper()
				transport := client.Transport.(*ValidatingTransport)
				httpTransport := transport.Transport.(*http.Transport)
				assert.NotNil(t, httpTransport.DialContext)
			},
		},
		{
			name: "invalid CA certificate file",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "invalid-ca.crt")
				require.NoError(t, os.WriteFile(tmpFile, []byte("invalid cert data"), 0644))
				return tmpFile, ""
			},
			expectError:   true,
			errorContains: "failed to parse CA certificate bundle",
		},
		{
			name: "missing CA certificate file",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(_ *testing.T) (string, string) {
				return "/nonexistent/ca.crt", ""
			},
			expectError:   true,
			errorContains: "failed to read CA certificate bundle",
		},
		{
			name: "missing token file",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(_ *testing.T) (string, string) {
				return "", "/nonexistent/token"
			},
			expectError:   true,
			errorContains: "failed to create token source",
		},
		{
			name: "empty token file",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "empty-token")
				require.NoError(t, os.WriteFile(tmpFile, []byte(""), 0644))
				return "", tmpFile
			},
			expectError:   true,
			errorContains: "auth token file is empty",
		},
		{
			name: "token file with whitespace only",
			setupBuilder: func() *HttpClientBuilder {
				return NewHttpClientBuilder()
			},
			setupFiles: func(t *testing.T) (string, string) {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "whitespace-token")
				require.NoError(t, os.WriteFile(tmpFile, []byte("   \n\t   "), 0644))
				return "", tmpFile
			},
			expectError:   true,
			errorContains: "auth token file is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder := tt.setupBuilder()
			caCertPath, tokenPath := tt.setupFiles(t)

			if caCertPath != "" {
				builder.WithCABundle(caCertPath)
			}
			if tokenPath != "" {
				builder.WithTokenFromFile(tokenPath)
			}

			client, err := builder.Build()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
				if tt.validateClient != nil {
					tt.validateClient(t, client)
				}
			}
		})
	}
}

func TestValidatingTransport_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		url               string
		insecureAllowHTTP bool
		expectError       bool
		errorContains     string
	}{
		{
			name:              "valid HTTPS URL",
			url:               "https://example.com/test",
			insecureAllowHTTP: false,
			expectError:       false,
		},
		{
			name:              "HTTP URL (not HTTPS)",
			url:               "http://example.com/test",
			insecureAllowHTTP: false,
			expectError:       true,
			errorContains:     "is not HTTPS scheme",
		},
		{
			name:              "malformed URL",
			url:               "not-a-url",
			insecureAllowHTTP: false,
			expectError:       true,
			errorContains:     "is not HTTPS scheme",
		},
		{
			name:              "HTTP URL allowed with InsecureAllowHTTP",
			url:               "http://localhost:8080/test",
			insecureAllowHTTP: true,
			expectError:       false,
		},
		{
			name:              "HTTPS URL still works with InsecureAllowHTTP",
			url:               "https://example.com/test",
			insecureAllowHTTP: true,
			expectError:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock transport
			mockTransport := &mockRoundTripper{
				response: &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("OK")),
				},
			}

			transport := &ValidatingTransport{
				Transport:         mockTransport,
				InsecureAllowHTTP: tt.insecureAllowHTTP,
			}

			req, err := http.NewRequest("GET", tt.url, nil)
			require.NoError(t, err)

			resp, err := transport.RoundTrip(req)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, resp)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.True(t, mockTransport.called)
			}
		})
	}
}

func TestOAuth2Transport_RoundTrip(t *testing.T) {
	t.Parallel()

	// Create a test server to capture the Authorization header
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("X-Auth-Header", auth)
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Create temp token file
	tokenFile := filepath.Join(t.TempDir(), "token")
	testToken := "test-bearer-token-123"
	require.NoError(t, os.WriteFile(tokenFile, []byte(testToken), 0644))

	// Create token source and oauth2 transport
	tokenSource, err := createTokenSourceFromFile(tokenFile)
	require.NoError(t, err)

	authTransport := &oauth2.Transport{
		Source: tokenSource,
		Base:   server.Client().Transport,
	}

	// Make request
	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := authTransport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Verify Authorization header was added
	expectedAuth := "Bearer " + testToken
	actualAuth := resp.Header.Get("X-Auth-Header")
	assert.Equal(t, expectedAuth, actualAuth)

	// Verify original request was not modified
	assert.Empty(t, req.Header.Get("Authorization"))
}

func TestCreateTokenSourceFromFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tokenContent  string
		expectError   bool
		errorContains string
		expectedToken string
	}{
		{
			name:          "valid token",
			tokenContent:  "valid-token-123",
			expectError:   false,
			expectedToken: "valid-token-123",
		},
		{
			name:          "token with trailing newline",
			tokenContent:  "token-with-newline\n",
			expectError:   false,
			expectedToken: "token-with-newline",
		},
		{
			name:          "token with whitespace",
			tokenContent:  "  token-with-spaces  \n\t",
			expectError:   false,
			expectedToken: "token-with-spaces",
		},
		{
			name:          "empty token",
			tokenContent:  "",
			expectError:   true,
			errorContains: "auth token file is empty",
		},
		{
			name:          "whitespace only token",
			tokenContent:  "   \n\t   ",
			expectError:   true,
			errorContains: "auth token file is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temp token file
			tokenFile := filepath.Join(t.TempDir(), "token")
			require.NoError(t, os.WriteFile(tokenFile, []byte(tt.tokenContent), 0644))

			tokenSource, err := createTokenSourceFromFile(tokenFile)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, tokenSource)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tokenSource)

				// Get token from source and verify
				token, err := tokenSource.Token()
				require.NoError(t, err)
				assert.Equal(t, tt.expectedToken, token.AccessToken)
				assert.Equal(t, "Bearer", token.TokenType)
			}
		})
	}

	t.Run("missing token file", func(t *testing.T) {
		t.Parallel()

		tokenSource, err := createTokenSourceFromFile("/nonexistent/token")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read auth token file")
		assert.Nil(t, tokenSource)
	})
}

// mockRoundTripper is a simple mock implementation of http.RoundTripper for testing
type mockRoundTripper struct {
	response *http.Response
	err      error
	called   bool
}

func (m *mockRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	m.called = true
	if m.err != nil {
		return nil, m.err
	}
	if m.response != nil {
		return m.response, nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("OK")),
	}, nil
}

func mustReq(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return &http.Request{URL: u, Host: u.Host}
}

func TestSameHostRedirectPolicy(t *testing.T) {
	t.Parallel()

	policy := SameHostRedirectPolicy()

	tests := []struct {
		name      string
		req       string
		via       []string
		wantRefus bool
	}{
		{
			name: "same host same scheme is allowed",
			req:  "https://mcp.example.com/next",
			via:  []string{"https://mcp.example.com/start"},
		},
		{
			name:      "same host different port is refused (distinct service)",
			req:       "https://mcp.example.com:8443/next",
			via:       []string{"https://mcp.example.com/start"},
			wantRefus: true,
		},
		{
			name:      "cross-host redirect to internal metadata endpoint is refused",
			req:       "http://169.254.169.254/latest/meta-data/",
			via:       []string{"https://mcp.example.com/.well-known/oauth-protected-resource"},
			wantRefus: true,
		},
		{
			name:      "cross-host redirect to another public host is refused",
			req:       "https://evil.example.net/x",
			via:       []string{"https://mcp.example.com/start"},
			wantRefus: true,
		},
		{
			name:      "https to http downgrade on same host is refused",
			req:       "http://mcp.example.com/next",
			via:       []string{"https://mcp.example.com/start"},
			wantRefus: true,
		},
		{
			name: "http to http on same host is allowed (no downgrade)",
			req:  "http://localhost:9000/next",
			via:  []string{"http://localhost:9000/start"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			via := make([]*http.Request, 0, len(tt.via))
			for _, v := range tt.via {
				via = append(via, mustReq(t, v))
			}
			err := policy(mustReq(t, tt.req), via)
			if tt.wantRefus {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrRedirectRefused)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSameHostRedirectPolicy_CapsChain(t *testing.T) {
	t.Parallel()

	policy := SameHostRedirectPolicy()
	via := make([]*http.Request, MaxRedirects)
	for i := range via {
		via[i] = mustReq(t, "https://mcp.example.com/start")
	}
	err := policy(mustReq(t, "https://mcp.example.com/next"), via)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRedirectRefused)
}

func TestSameHostRedirectPolicy_Integration(t *testing.T) {
	t.Parallel()

	// internal stands in for an internal-only endpoint a malicious server
	// would try to reach via a cross-host redirect.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("SECRET"))
	}))
	t.Cleanup(internal.Close)

	// attacker redirects any request to the internal endpoint (cross-host).
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			// same-host relative redirect should be followed
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		if r.URL.Path == "/ok" {
			_, _ = w.Write([]byte("OK"))
			return
		}
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	t.Cleanup(attacker.Close)

	client := &http.Client{CheckRedirect: SameHostRedirectPolicy()}

	t.Run("cross-host redirect is refused", func(t *testing.T) {
		t.Parallel()
		resp, err := client.Get(attacker.URL + "/evil") //nolint:bodyclose // err path, no body
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRedirectRefused)
		if resp != nil {
			_ = resp.Body.Close()
		}
	})

	t.Run("same-host redirect is followed", func(t *testing.T) {
		t.Parallel()
		resp, err := client.Get(attacker.URL + "/same")
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "OK", string(body))
	})
}
