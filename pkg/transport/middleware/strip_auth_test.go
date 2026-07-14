// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/transport/types"
	typesmocks "github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// TestStripAuthMiddleware covers the strip-auth middleware end to end: the
// factory registers it on the runner under the config type, the handler
// removes every client credential header (Authorization, Cookie,
// Proxy-Authorization) while passing unrelated headers through, and Close is
// a no-op.
func TestStripAuthMiddleware(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	var registered types.Middleware
	mockRunner := typesmocks.NewMockMiddlewareRunner(ctrl)
	mockRunner.EXPECT().AddMiddleware(StripAuthMiddlewareName, gomock.Any()).Do(func(_ string, mw types.Middleware) {
		registered = mw
	})

	mwConfig, err := types.NewMiddlewareConfig(StripAuthMiddlewareName, struct{}{})
	require.NoError(t, err)
	require.NoError(t, CreateStripAuthMiddleware(mwConfig, mockRunner))
	require.NotNil(t, registered)

	var captured *http.Request
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer toolhive-jwt")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Proxy-Authorization", "Basic Zm9vOmJhcg==")
	req.Header.Set("X-Custom", "kept")
	rec := httptest.NewRecorder()

	registered.Handler()(next).ServeHTTP(rec, req)

	require.NotNil(t, captured)
	for _, h := range clientCredentialHeaders {
		assert.Empty(t, captured.Header.Get(h), "%s must be stripped before reaching the backend", h)
	}
	assert.Equal(t, "kept", captured.Header.Get("X-Custom"), "unrelated headers must pass through")
	assert.Equal(t, http.StatusNoContent, rec.Code)

	assert.NoError(t, registered.Close())
}
