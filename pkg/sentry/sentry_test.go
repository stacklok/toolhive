// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sentry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests are deliberately NOT parallel because they mutate the package-level
// `initialized` atomic, which is global shared state.

//nolint:paralleltest // mutates global initialized state
func TestInit(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantEnabled bool
		wantErr     bool
	}{
		{
			name:        "empty DSN is a no-op",
			cfg:         Config{},
			wantEnabled: false,
		},
		{
			name: "valid DSN initializes Sentry",
			cfg: Config{
				DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
				Environment:      "test",
				TracesSampleRate: 1.0,
			},
			wantEnabled: true,
		},
		{
			name: "invalid DSN returns error",
			cfg: Config{
				DSN: "not-a-valid-dsn",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initialized.Store(false)
			defer initialized.Store(false)

			err := Init(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, Enabled())
		})
	}
}

//nolint:paralleltest // mutates global initialized state
func TestClose(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		Close()
	})

	t.Run("flushes when initialized", func(t *testing.T) {
		initialized.Store(false)
		err := Init(Config{
			DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
			Environment:      "test",
			TracesSampleRate: 1.0,
		})
		require.NoError(t, err)
		defer initialized.Store(false)

		Close()
	})
}

//nolint:paralleltest // uses sentryhttp which depends on global Sentry state
func TestNewMiddleware(t *testing.T) {
	mw := NewMiddleware()
	require.NotNil(t, mw)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

//nolint:paralleltest // mutates global initialized state
func TestNewMiddleware_ExtractsTraceHeaders(t *testing.T) {
	initialized.Store(false)
	err := Init(Config{
		DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
		TracesSampleRate: 1.0,
	})
	require.NoError(t, err)
	defer initialized.Store(false)

	var hubFromCtx *sentry.Hub

	mw := NewMiddleware()
	handler := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hubFromCtx = sentry.GetHubFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("sentry-trace", "d49d9bf66f13450b81f65bc51cf49c03-1cc3b0c945714681-1")
	req.Header.Set("baggage", "sentry-environment=production")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.NotNil(t, hubFromCtx, "Sentry hub should be attached to request context")
}

//nolint:paralleltest // mutates global initialized state
func TestCaptureException(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		CaptureException(req, errors.New("test error"))
	})

	t.Run("no-op with nil error", func(_ *testing.T) {
		initialized.Store(true)
		defer initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		CaptureException(req, nil)
	})
}

//nolint:paralleltest // mutates global initialized state
func TestRecoverPanic(t *testing.T) {
	t.Run("no-op when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		RecoverPanic(req, "test panic")
	})

	t.Run("no-op with nil recovered value", func(_ *testing.T) {
		initialized.Store(true)
		defer initialized.Store(false)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		RecoverPanic(req, nil)
	})
}

//nolint:paralleltest // mutates global initialized state
func TestEnabled(t *testing.T) {
	initialized.Store(false)
	assert.False(t, Enabled())

	initialized.Store(true)
	assert.True(t, Enabled())
	initialized.Store(false)
}
