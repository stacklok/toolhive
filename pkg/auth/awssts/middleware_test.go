package awssts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// TestCreateMiddleware tests the factory function validation.
func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      MiddlewareParams
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil config returns error",
			params:      MiddlewareParams{AWSStsConfig: nil},
			expectError: true,
			errorMsg:    "AWS STS configuration is required",
		},
		{
			name: "missing region returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{RoleArn: "arn:aws:iam::123456789012:role/TestRole"},
			},
			expectError: true,
			errorMsg:    "AWS region is required",
		},
		{
			name: "invalid role ARN format returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{Region: "us-east-1", RoleArn: "invalid-arn"},
			},
			expectError: true,
			errorMsg:    "invalid IAM role ARN format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

			paramsJSON, err := json.Marshal(tt.params)
			require.NoError(t, err)

			config := &types.MiddlewareConfig{Type: MiddlewareType, Parameters: paramsJSON}
			err = CreateMiddleware(config, mockRunner)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errorMsg)
		})
	}
}

// TestMiddleware_Close tests that Close clears the cache.
func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	cache := NewCredentialCache(10)
	cache.Set("role1", "token1", &Credentials{
		AccessKeyID: "AKIATEST", SecretAccessKey: "secret",
		SessionToken: "token", Expiration: time.Now().Add(time.Hour),
	})

	mw := &Middleware{cache: cache}
	assert.Equal(t, 1, cache.Size())

	err := mw.Close()
	assert.NoError(t, err)
	assert.Equal(t, 0, cache.Size())
}

// TestMiddlewareFunc_PassThrough tests pass-through scenarios.
func TestMiddlewareFunc_PassThrough(t *testing.T) {
	t.Parallel()

	mockClient := &mockSTSClient{}
	exchanger, _ := NewExchangerWithClient(mockClient, "us-east-1")
	cache := NewCredentialCache(10)
	roleMapper := NewRoleMapper(&Config{Region: "us-east-1", RoleArn: "arn:aws:iam::123456789012:role/TestRole"})
	signer := NewSigner("us-east-1", "aws-mcp")

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, cache, roleMapper, signer, "sub", 3600, nil)

	tests := []struct {
		name    string
		setupFn func(*http.Request) *http.Request
	}{
		{
			name:    "no identity in context",
			setupFn: func(r *http.Request) *http.Request { return r },
		},
		{
			name: "no bearer token",
			setupFn: func(r *http.Request) *http.Request {
				identity := &auth.Identity{Subject: "user123", Claims: map[string]interface{}{"sub": "user123"}}
				return r.WithContext(auth.WithIdentity(r.Context(), identity))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handlerCalled := false
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req = tt.setupFn(req)

			rec := httptest.NewRecorder()
			middlewareFunc(testHandler).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.True(t, handlerCalled)
		})
	}
}

// TestMiddlewareFunc_SignsRequest tests successful STS exchange and SigV4 signing.
func TestMiddlewareFunc_SignsRequest(t *testing.T) {
	t.Parallel()

	expiration := time.Now().Add(time.Hour)
	mockClient := &mockSTSClient{
		response: &sts.AssumeRoleWithWebIdentityOutput{
			Credentials: &ststypes.Credentials{
				AccessKeyId: strPtr("AKIATEST"), SecretAccessKey: strPtr("secret"),
				SessionToken: strPtr("session"), Expiration: &expiration,
			},
		},
	}

	exchanger, _ := NewExchangerWithClient(mockClient, "us-east-1")
	cache := NewCredentialCache(10)
	roleMapper := NewRoleMapper(&Config{Region: "us-east-1", RoleArn: "arn:aws:iam::123456789012:role/TestRole"})
	signer := NewSigner("us-east-1", "aws-mcp")

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, cache, roleMapper, signer, "sub", 3600, nil)

	var capturedAuthHeader string
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	req.Header.Set("Authorization", "Bearer test-jwt-token")
	identity := &auth.Identity{Subject: "user123", Claims: map[string]interface{}{"sub": "user123"}}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rec := httptest.NewRecorder()
	middlewareFunc(testHandler).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, capturedAuthHeader, "AWS4-HMAC-SHA256")
	assert.NotNil(t, cache.Get("arn:aws:iam::123456789012:role/TestRole", "test-jwt-token"))
}

// TestMiddlewareFunc_STSFailure tests handling of STS exchange failure.
func TestMiddlewareFunc_STSFailure(t *testing.T) {
	t.Parallel()

	mockClient := &mockSTSClient{err: ErrAccessDenied}
	exchanger, _ := NewExchangerWithClient(mockClient, "us-east-1")
	cache := NewCredentialCache(10)
	roleMapper := NewRoleMapper(&Config{Region: "us-east-1", RoleArn: "arn:aws:iam::123456789012:role/TestRole"})
	signer := NewSigner("us-east-1", "aws-mcp")

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, cache, roleMapper, signer, "sub", 3600, nil)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-jwt-token")
	identity := &auth.Identity{Subject: "user123", Claims: map[string]interface{}{"sub": "user123"}}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rec := httptest.NewRecorder()
	middlewareFunc(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestSanitizeSessionName tests session name sanitization.
func TestSanitizeSessionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input, expected string
	}{
		{"user-123_test@example.com", "user-123_test@example.com"},
		{"user:name/path", "user_name_path"},
		{"", "toolhive-session"},
		{"this-is-a-very-long-session-name-that-exceeds-the-maximum-allowed-length-of-64-chars", "this-is-a-very-long-session-name-that-exceeds-the-maximum-allowe"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, sanitizeSessionName(tt.input))
	}
}

// TestMiddlewareFunc_SignsRequestWithTargetURL tests that the middleware correctly sets
// the request URL and Host to the target URL before SigV4 signing.
func TestMiddlewareFunc_SignsRequestWithTargetURL(t *testing.T) {
	t.Parallel()

	expiration := time.Now().Add(time.Hour)
	mockClient := &mockSTSClient{
		response: &sts.AssumeRoleWithWebIdentityOutput{
			Credentials: &ststypes.Credentials{
				AccessKeyId: strPtr("AKIATEST"), SecretAccessKey: strPtr("secret"),
				SessionToken: strPtr("session"), Expiration: &expiration,
			},
		},
	}

	exchanger, _ := NewExchangerWithClient(mockClient, "us-east-1")
	cache := NewCredentialCache(10)
	roleMapper := NewRoleMapper(&Config{Region: "us-east-1", RoleArn: "arn:aws:iam::123456789012:role/TestRole"})
	signer := NewSigner("us-east-1", "aws-mcp")

	// Parse target URL for AWS MCP Server
	targetURL, err := url.Parse("https://aws-mcp.us-east-1.api.aws")
	require.NoError(t, err)

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, cache, roleMapper, signer, "sub", 3600, targetURL)

	var capturedHost string
	var capturedURLHost string
	var capturedScheme string
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHost = r.Host
		capturedURLHost = r.URL.Host
		capturedScheme = r.URL.Scheme
		w.WriteHeader(http.StatusOK)
	})

	// Create request with proxy URL (localhost:8080) - simulating what the proxy receives
	req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/mcp/v1", nil)
	req.Header.Set("Authorization", "Bearer test-jwt-token")
	identity := &auth.Identity{Subject: "user123", Claims: map[string]interface{}{"sub": "user123"}}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rec := httptest.NewRecorder()
	middlewareFunc(testHandler).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	// Verify that the request URL was updated to the target before signing
	assert.Equal(t, "aws-mcp.us-east-1.api.aws", capturedHost, "Host should be set to target")
	assert.Equal(t, "aws-mcp.us-east-1.api.aws", capturedURLHost, "URL.Host should be set to target")
	assert.Equal(t, "https", capturedScheme, "URL.Scheme should be set to https")
}
