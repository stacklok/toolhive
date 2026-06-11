// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package bodylimit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestMiddleware(t *testing.T) {
	t.Parallel()
	const maxBodySize = 1 << 20 // 1MB

	createHandler := func(next http.Handler) http.Handler {
		return Middleware(maxBodySize)(next)
	}

	readBodyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, err := buf.ReadFrom(r.Body)
		assert.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	})

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("Request body within limit", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBuffer(make([]byte, maxBodySize-1))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		createHandler(readBodyHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Request body exactly at limit", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBuffer(make([]byte, maxBodySize))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		createHandler(readBodyHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Request body exceeds limit via Content-Length", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewBuffer(make([]byte, maxBodySize+1))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		createHandler(okHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "Request Entity Too Large")
	})

	t.Run("MaxBytesReader converts handler 400 to 413 when limit exceeded", func(t *testing.T) {
		t.Parallel()
		// Valid JSON larger than the limit so the decoder reads past the cap.
		largeArray := "["
		for i := 0; i < 100000; i++ {
			if i > 0 {
				largeArray += ","
			}
			largeArray += `{"key":"value"}`
		}
		largeArray += "]"

		body := bytes.NewBuffer([]byte(largeArray))
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", body)
		// Lie about Content-Length to bypass the early check and exercise MaxBytesReader.
		req.ContentLength = maxBodySize - 1
		rec := httptest.NewRecorder()

		decodeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data []map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				http.Error(w, "Failed to decode request", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		createHandler(decodeHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("Validation 400 on exactly-at-limit body stays 400", func(t *testing.T) {
		t.Parallel()
		// A body of EXACTLY the limit is within bounds: http.MaxBytesReader
		// returns no error, so a genuine handler 400 must NOT be rewritten to
		// 413. This is the regression guard for the removed byte-counter
		// heuristic (F1).
		body := bytes.NewBuffer(make([]byte, maxBodySize))
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", body)
		rec := httptest.NewRecorder()

		validateHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Drain the full body so the reader observes the exactly-at-limit
			// size, then reject for validation reasons.
			_, err := io.Copy(io.Discard, r.Body)
			assert.NoError(t, err)
			http.Error(w, "Validation failed", http.StatusBadRequest)
		})

		createHandler(validateHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "Validation failed")
	})

	t.Run("Empty request body succeeds", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer([]byte{}))
		rec := httptest.NewRecorder()

		createHandler(okHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Validation errors return 400, not 413", func(t *testing.T) {
		t.Parallel()
		// A small, valid JSON body well within the limit that the handler rejects
		// for validation reasons. The 400 must NOT be converted to 413.
		body := bytes.NewBuffer([]byte(`{"name":""}`))
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/workloads", body)
		rec := httptest.NewRecorder()

		validateHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				http.Error(w, "Failed to decode request", http.StatusBadRequest)
				return
			}
			name, ok := data["name"].(string)
			if !ok || name == "" {
				http.Error(w, "Validation failed: name cannot be empty", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		createHandler(validateHandler).ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "Validation failed")
	})
}

// TestMiddleware_NonPositiveLimitFallsBackToDefault verifies that a zero or
// negative limit is treated as the default cap, never as "unlimited".
func TestMiddleware_NonPositiveLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxBytes int64
	}{
		{name: "zero", maxBytes: 0},
		{name: "negative", maxBytes: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A body one byte over the default must be rejected, proving the
			// default cap was applied rather than "unlimited".
			body := bytes.NewBuffer(make([]byte, DefaultMaxRequestBodySize+1))
			req := httptest.NewRequest(http.MethodPost, "/test", body)
			rec := httptest.NewRecorder()

			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			Middleware(tt.maxBytes)(next).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		})
	}
}

// deadlineResponseWriter is a fake http.ResponseWriter that also implements
// SetWriteDeadline so http.ResponseController can reach it through Unwrap.
type deadlineResponseWriter struct {
	http.ResponseWriter
	setWriteDeadlineCalled bool
}

func (w *deadlineResponseWriter) SetWriteDeadline(time.Time) error {
	w.setWriteDeadlineCalled = true
	return nil
}

// TestBodySizeResponseWriter_UnwrapEnablesResponseController verifies that
// Unwrap exposes the underlying writer so http.ResponseController can reach
// SetWriteDeadline — the chain SSE WriteTimeout clearing depends on.
func TestBodySizeResponseWriter_UnwrapEnablesResponseController(t *testing.T) {
	t.Parallel()

	fake := &deadlineResponseWriter{ResponseWriter: httptest.NewRecorder()}
	wrapped := &bodySizeResponseWriter{ResponseWriter: fake, limitExceeded: new(bool)}

	err := http.NewResponseController(wrapped).SetWriteDeadline(time.Time{})

	require.NoError(t, err)
	assert.NotErrorIs(t, err, http.ErrNotSupported)
	assert.True(t, fake.setWriteDeadlineCalled, "expected SetWriteDeadline to reach the underlying writer via Unwrap")
}

// flushResponseWriter is a fake http.ResponseWriter that implements http.Flusher.
type flushResponseWriter struct {
	http.ResponseWriter
	flushCalled bool
}

func (w *flushResponseWriter) Flush() {
	w.flushCalled = true
}

// nonFlusherResponseWriter is a fake http.ResponseWriter that does NOT implement
// http.Flusher.
type nonFlusherResponseWriter struct{}

func (nonFlusherResponseWriter) Header() http.Header         { return http.Header{} }
func (nonFlusherResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nonFlusherResponseWriter) WriteHeader(int)             {}

// TestBodySizeResponseWriter_FlushForwards verifies Flush forwards to a
// Flusher-capable underlying writer (needed for SSE) and is a safe no-op
// otherwise.
func TestBodySizeResponseWriter_FlushForwards(t *testing.T) {
	t.Parallel()

	t.Run("forwards to underlying Flusher", func(t *testing.T) {
		t.Parallel()
		fake := &flushResponseWriter{ResponseWriter: httptest.NewRecorder()}
		wrapped := &bodySizeResponseWriter{ResponseWriter: fake, limitExceeded: new(bool)}

		wrapped.Flush()

		assert.True(t, fake.flushCalled, "expected Flush to forward to the underlying Flusher")
	})

	t.Run("no panic when underlying writer is not a Flusher", func(t *testing.T) {
		t.Parallel()
		wrapped := &bodySizeResponseWriter{ResponseWriter: nonFlusherResponseWriter{}, limitExceeded: new(bool)}

		assert.NotPanics(t, wrapped.Flush)
	})
}

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
