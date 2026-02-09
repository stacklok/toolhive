// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
		name     string
		params   MiddlewareParams
		errorMsg string
	}{
		{
			name:     "nil config returns error",
			params:   MiddlewareParams{AWSStsConfig: nil},
			errorMsg: "AWS STS configuration is required",
		},
		{
			name: "missing region returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole"},
			},
			errorMsg: "AWS region is required",
		},
		{
			name: "invalid role ARN format returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{Region: "us-east-1", FallbackRoleArn: "invalid-arn"},
			},
			errorMsg: "invalid IAM role ARN format",
		},
		{
			name: "target URL missing scheme and host returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{Region: "us-east-1", FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole"},
				TargetURL:    "example.com/path",
			},
			errorMsg: "target URL must include scheme and host",
		},
		{
			name: "target URL missing host returns error",
			params: MiddlewareParams{
				AWSStsConfig: &Config{Region: "us-east-1", FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole"},
				TargetURL:    "/just-a-path",
			},
			errorMsg: "target URL must include scheme and host",
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

// TestCreateMiddleware_Success tests the factory function happy path.
func TestCreateMiddleware_Success(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
	mockRunner.EXPECT().AddMiddleware(MiddlewareType, gomock.Any()).Times(1)

	params := MiddlewareParams{
		AWSStsConfig: &Config{
			Region:          "us-east-1",
			FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
		},
	}

	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	config := &types.MiddlewareConfig{Type: MiddlewareType, Parameters: paramsJSON}
	err = CreateMiddleware(config, mockRunner)

	require.NoError(t, err)
}

// TestMiddlewareFunc_RejectsUnauthenticated tests that requests without proper
// authentication are rejected when the middleware is configured.
func TestMiddlewareFunc_RejectsUnauthenticated(t *testing.T) {
	t.Parallel()

	exchanger := &Exchanger{client: &mockSTSClient{}}
	roleMapper, _ := NewRoleMapper(&Config{Region: "us-east-1", FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole"})
	signer, _ := newRequestSigner("us-east-1")

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, roleMapper, signer, "sub", 3600, nil)

	tests := []struct {
		name    string
		setupFn func(*http.Request) *http.Request
	}{
		{
			name:    "no identity in context",
			setupFn: func(r *http.Request) *http.Request { return r },
		},
		{
			name: "identity with nil claims",
			setupFn: func(r *http.Request) *http.Request {
				identity := &auth.Identity{Subject: "user123", Claims: nil}
				return r.WithContext(auth.WithIdentity(r.Context(), identity))
			},
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

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, handlerCalled)
		})
	}
}

// TestMiddlewareFunc_EndToEnd tests the full middleware flow: STS exchange,
// SigV4 signing, target URL rewriting, and STS failure handling.
func TestMiddlewareFunc_EndToEnd(t *testing.T) {
	t.Parallel()

	expiration := time.Now().Add(time.Hour)
	successResponse := &sts.AssumeRoleWithWebIdentityOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId: aws.String("AKIATEST"), SecretAccessKey: aws.String("secret"),
			SessionToken: aws.String("session"), Expiration: &expiration,
		},
	}

	targetURL, err := url.Parse("https://aws-mcp.us-east-1.api.aws")
	require.NoError(t, err)

	tests := []struct {
		name           string
		mockClient     *mockSTSClient
		targetURL      *url.URL
		requestURL     string
		requestBody    string // optional body to send with the request
		wantStatus     int
		wantAuthPrefix string
		// wantOrigHost/Scheme assert that the middleware does NOT overwrite
		// the original request's Host and URL fields — that is the reverse
		// proxy's responsibility.
		wantOrigHost   string
		wantOrigScheme string
		// wantBodyPreserved, if non-empty, asserts that the next handler
		// can still read the request body after signing.
		wantBodyPreserved string
	}{
		{
			name:           "signs request successfully",
			mockClient:     &mockSTSClient{response: successResponse},
			requestURL:     "http://example.com/test",
			wantStatus:     http.StatusOK,
			wantAuthPrefix: "AWS4-HMAC-SHA256",
		},
		{
			name:       "returns 401 on STS failure",
			mockClient: &mockSTSClient{err: ErrAccessDenied},
			requestURL: "/test",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:           "signs for target without rewriting host",
			mockClient:     &mockSTSClient{response: successResponse},
			targetURL:      targetURL,
			requestURL:     "http://localhost:8080/mcp/v1",
			wantStatus:     http.StatusOK,
			wantAuthPrefix: "AWS4-HMAC-SHA256",
			wantOrigHost:   "localhost:8080",
			wantOrigScheme: "http",
		},
		{
			name:              "signs for target with body preserving it for downstream",
			mockClient:        &mockSTSClient{response: successResponse},
			targetURL:         targetURL,
			requestURL:        "http://localhost:8080/mcp/v1",
			requestBody:       `{"method":"tools/list","params":{}}`,
			wantStatus:        http.StatusOK,
			wantAuthPrefix:    "AWS4-HMAC-SHA256",
			wantOrigHost:      "localhost:8080",
			wantOrigScheme:    "http",
			wantBodyPreserved: `{"method":"tools/list","params":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exchanger := &Exchanger{client: tt.mockClient}
			roleMapper, _ := NewRoleMapper(&Config{Region: "us-east-1", FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole"})
			signer, _ := newRequestSigner("us-east-1")

			middlewareFunc := createAWSStsMiddlewareFunc(exchanger, roleMapper, signer, "sub", 3600, tt.targetURL)

			var capturedAuth, capturedHost, capturedURLHost, capturedScheme, capturedBody string
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedAuth = r.Header.Get("Authorization")
				capturedHost = r.Host
				capturedURLHost = r.URL.Host
				capturedScheme = r.URL.Scheme
				if r.Body != nil {
					b, _ := io.ReadAll(r.Body)
					capturedBody = string(b)
				}
				w.WriteHeader(http.StatusOK)
			})

			var bodyReader io.Reader
			if tt.requestBody != "" {
				bodyReader = strings.NewReader(tt.requestBody)
			}
			req := httptest.NewRequest(http.MethodPost, tt.requestURL, bodyReader)
			req.Header.Set("Authorization", "Bearer test-jwt-token")
			identity := &auth.Identity{Subject: "user123", Claims: map[string]interface{}{"sub": "user123"}}
			req = req.WithContext(auth.WithIdentity(req.Context(), identity))

			rec := httptest.NewRecorder()
			middlewareFunc(testHandler).ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			if tt.wantAuthPrefix != "" {
				assert.Contains(t, capturedAuth, tt.wantAuthPrefix)
			}
			if tt.wantOrigHost != "" {
				assert.Equal(t, tt.wantOrigHost, capturedHost, "Host should not be overwritten by middleware")
				assert.Equal(t, tt.wantOrigHost, capturedURLHost, "URL.Host should not be overwritten by middleware")
			}
			if tt.wantOrigScheme != "" {
				assert.Equal(t, tt.wantOrigScheme, capturedScheme, "URL.Scheme should not be overwritten by middleware")
			}
			if tt.wantBodyPreserved != "" {
				assert.Equal(t, tt.wantBodyPreserved, capturedBody, "Request body should be preserved after signing")
			}
		})
	}
}

// TestMiddlewareFunc_RoleMapperFailure tests that the middleware returns 403
// when the role mapper cannot determine an IAM role for the request.
func TestMiddlewareFunc_RoleMapperFailure(t *testing.T) {
	t.Parallel()

	exchanger := &Exchanger{client: &mockSTSClient{}}
	// No fallback role, only a mapping for "admins" group — claims won't match.
	roleMapper, err := NewRoleMapper(&Config{
		Region:    "us-east-1",
		RoleClaim: "groups",
		RoleMappings: []RoleMapping{
			{Claim: "admins", RoleArn: "arn:aws:iam::123456789012:role/AdminRole"},
		},
	})
	require.NoError(t, err)

	signer, err := newRequestSigner("us-east-1")
	require.NoError(t, err)

	middlewareFunc := createAWSStsMiddlewareFunc(exchanger, roleMapper, signer, "sub", 3600, nil)

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-jwt-token")
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]interface{}{
			"sub":    "user123",
			"groups": []interface{}{"developers"}, // Does not match "admins"
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rec := httptest.NewRecorder()
	middlewareFunc(testHandler).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, handlerCalled)
}

// TestExtractSessionName tests session name extraction from JWT claims.
func TestExtractSessionName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claims    map[string]interface{}
		claimName string
		want      string
		wantErr   bool
	}{
		{
			name:      "returns claim value",
			claims:    map[string]interface{}{"sub": "user@example.com"},
			claimName: "sub",
			want:      "user@example.com",
		},
		{
			name:      "missing claim returns error",
			claims:    map[string]interface{}{"email": "user@example.com"},
			claimName: "sub",
			wantErr:   true,
		},
		{
			name:      "empty string claim returns error",
			claims:    map[string]interface{}{"sub": ""},
			claimName: "sub",
			wantErr:   true,
		},
		{
			name:      "non-string claim returns error",
			claims:    map[string]interface{}{"sub": 12345},
			claimName: "sub",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractSessionName(tt.claims, tt.claimName)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
