// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// stubTokenSource implements oauth2.TokenSource for testing.
type stubTokenSource struct {
	token *oauth2.Token
	err   error
}

func (s *stubTokenSource) Token() (*oauth2.Token, error) {
	return s.token, s.err
}

func TestCreateTokenInjectionMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		tokenSource     oauth2.TokenSource
		wantStatus      int
		wantNextCalled  bool
		wantAuthHeader  string
		wantRetryAfter  string
		wantBodyContain string
	}{
		{
			name: "token source error returns 503 with Retry-After",
			tokenSource: &stubTokenSource{
				err: errors.New("token expired"),
			},
			wantStatus:      http.StatusServiceUnavailable,
			wantNextCalled:  false,
			wantRetryAfter:  "10",
			wantBodyContain: "Token temporarily unavailable",
		},
		{
			name: "token source succeeds injects Bearer token",
			tokenSource: &stubTokenSource{
				token: &oauth2.Token{AccessToken: "test-access-token"},
			},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantAuthHeader: "Bearer test-access-token",
		},
		{
			name:           "nil token source passes request through",
			tokenSource:    nil,
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantAuthHeader: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nextCalled := false
			var capturedReq *http.Request

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				capturedReq = r
				w.WriteHeader(http.StatusOK)
			})

			mw := CreateTokenInjectionMiddleware(tt.tokenSource)
			handler := mw(next)

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantNextCalled, nextCalled)

			if tt.wantRetryAfter != "" {
				assert.Equal(t, tt.wantRetryAfter, rec.Header().Get("Retry-After"))
			}

			if tt.wantBodyContain != "" {
				assert.Contains(t, rec.Body.String(), tt.wantBodyContain)
			}

			if tt.wantNextCalled {
				require.NotNil(t, capturedReq)
				if tt.wantAuthHeader != "" {
					assert.Equal(t, tt.wantAuthHeader, capturedReq.Header.Get("Authorization"))
				} else {
					assert.Empty(t, capturedReq.Header.Get("Authorization"))
				}
			}
		})
	}
}
