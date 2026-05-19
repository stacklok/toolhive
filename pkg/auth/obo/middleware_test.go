// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// withDefaultFactory swaps the package-level CreateMiddleware for the duration
// of a test, restoring the original on cleanup so tests that mutate the global
// state do not leak into each other.
func withDefaultFactory(t *testing.T) {
	t.Helper()
	original := CreateMiddleware
	t.Cleanup(func() { CreateMiddleware = original })
}

func TestMiddlewareType(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "obo", MiddlewareType,
		"MiddlewareType must equal the ExternalAuthType value 'obo'")
}

func TestDefaultCreateMiddleware_AddsStub(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
	mockRunner.EXPECT().AddMiddleware(MiddlewareType, gomock.Any()).Times(1)

	cfg := &types.MiddlewareConfig{Type: MiddlewareType}
	require.NoError(t, CreateMiddleware(cfg, mockRunner))
}

func TestStubHandler_Returns503(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		downstream http.Handler
		method     string
		path       string
	}{
		{
			name:       "nil downstream handler is ignored",
			downstream: nil,
			method:     http.MethodGet,
			path:       "/anything",
		},
		{
			name: "non-nil downstream handler is not invoked",
			downstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// If this is ever reached, the stub leaked through — the test
				// asserts on the response code below to catch that.
				w.WriteHeader(http.StatusTeapot)
			}),
			method: http.MethodPost,
			path:   "/whatever",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &stub{}
			wrapper := s.Handler()
			require.NotNil(t, wrapper)

			h := wrapper(tt.downstream)
			require.NotNil(t, h)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
				"stub handler must respond 503, not call downstream")
			// http.Error appends a newline to the supplied message; assert on
			// the exact body so the cross-file contract with stubMessage stays
			// pinned (any change to stubMessage that wasn't intentional
			// breaks this test loudly).
			assert.Equal(t, stubMessage+"\n", rec.Body.String(),
				"stub handler must echo the package stubMessage constant")
		})
	}
}

func TestStub_Close(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (&stub{}).Close(), "stub Close must be a no-op")
}

//nolint:paralleltest // Mutates package-level CreateMiddleware; must not race other tests.
func TestRegisterFactory_ReplacesDefault(t *testing.T) {
	withDefaultFactory(t)

	var called bool
	replacement := types.MiddlewareFactory(func(*types.MiddlewareConfig, types.MiddlewareRunner) error {
		called = true
		return nil
	})
	RegisterFactory(replacement)

	// Calling through CreateMiddleware after RegisterFactory must dispatch to
	// the replacement.
	require.NoError(t, CreateMiddleware(&types.MiddlewareConfig{Type: MiddlewareType}, nil))
	assert.True(t, called, "RegisterFactory must replace CreateMiddleware")
}

//nolint:paralleltest // Mutates package-level CreateMiddleware; must not race other tests.
func TestRegisterFactory_LastWriteWins(t *testing.T) {
	withDefaultFactory(t)

	sentinel := errors.New("second factory")
	RegisterFactory(func(*types.MiddlewareConfig, types.MiddlewareRunner) error {
		return errors.New("first factory")
	})
	RegisterFactory(func(*types.MiddlewareConfig, types.MiddlewareRunner) error {
		return sentinel
	})

	err := CreateMiddleware(&types.MiddlewareConfig{Type: MiddlewareType}, nil)
	require.ErrorIs(t, err, sentinel, "second RegisterFactory must overwrite the first")
}
