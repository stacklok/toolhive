// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sentry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gosentry "github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/telemetry"
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

//nolint:paralleltest // mutates global initialized and telemetry registry state
func TestInit_RegistersSpanProcessor(t *testing.T) {
	t.Run("does not register processor when not initialized", func(_ *testing.T) {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()
		assert.False(t, telemetry.HasRegisteredSpanProcessors())
	})

	t.Run("registers span processor with telemetry registry on init", func(t *testing.T) {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()
		err := Init(Config{
			DSN:              "https://examplePublicKey@o0.ingest.sentry.io/0",
			TracesSampleRate: 1.0,
		})
		require.NoError(t, err)
		defer func() {
			initialized.Store(false)
			telemetry.ResetSpanProcessorsForTesting()
		}()

		assert.True(t, telemetry.HasRegisteredSpanProcessors())
	})
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

	t.Run("captures exception when initialized", func(t *testing.T) {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()

		transport := &gosentry.MockTransport{}
		err := gosentry.Init(gosentry.ClientOptions{
			Dsn:       "https://examplePublicKey@o0.ingest.sentry.io/0",
			Transport: transport,
		})
		require.NoError(t, err)
		initialized.Store(true)
		defer func() {
			initialized.Store(false)
			telemetry.ResetSpanProcessorsForTesting()
		}()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		CaptureException(req, errors.New("test capture"))

		// hub.CaptureException enqueues the event; Flush delivers it to the transport.
		gosentry.Flush(flushTimeout)
		assert.Equal(t, 1, len(transport.Events()))
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

	t.Run("recovers panic and creates Sentry event", func(t *testing.T) {
		initialized.Store(false)
		telemetry.ResetSpanProcessorsForTesting()

		transport := &gosentry.MockTransport{}
		err := gosentry.Init(gosentry.ClientOptions{
			Dsn:       "https://examplePublicKey@o0.ingest.sentry.io/0",
			Transport: transport,
		})
		require.NoError(t, err)
		initialized.Store(true)
		defer func() {
			initialized.Store(false)
			telemetry.ResetSpanProcessorsForTesting()
		}()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// RecoverPanic calls hub.Flush internally so events should be
		// immediately available on the transport after the call returns.
		RecoverPanic(req, "test panic value")

		assert.Equal(t, 1, len(transport.Events()))
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
