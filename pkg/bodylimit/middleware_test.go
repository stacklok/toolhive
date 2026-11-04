// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package bodylimit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// fakeRunner is a minimal types.MiddlewareRunner that records added middleware.
type fakeRunner struct {
	types.MiddlewareRunner
	added map[string]types.Middleware
}

func (f *fakeRunner) AddMiddleware(name string, mw types.Middleware) {
	if f.added == nil {
		f.added = make(map[string]types.Middleware)
	}
	f.added[name] = mw
}

// TestCreateMiddleware verifies the registry factory builds and registers a
// working body-limit middleware from serialized parameters.
func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	cfg, err := types.NewMiddlewareConfig(MiddlewareType, MiddlewareParams{MaxBytes: 1 << 20})
	require.NoError(t, err)

	runner := &fakeRunner{}
	require.NoError(t, CreateMiddleware(cfg, runner))

	mw, ok := runner.added[MiddlewareType]
	require.True(t, ok, "expected middleware to be registered under %q", MiddlewareType)
	require.NotNil(t, mw.Handler())
	require.NoError(t, mw.Close())

	// The registered handler must reject an oversized body.
	body := bytes.NewBuffer(make([]byte, (1<<20)+1))
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	rec := httptest.NewRecorder()
	mw.Handler()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// TestCreateMiddleware_InvalidParams verifies malformed parameters surface an error.
func TestCreateMiddleware_InvalidParams(t *testing.T) {
	t.Parallel()

	cfg := &types.MiddlewareConfig{Type: MiddlewareType, Parameters: json.RawMessage(`not json`)}
	err := CreateMiddleware(cfg, &fakeRunner{})
	require.Error(t, err)
}
