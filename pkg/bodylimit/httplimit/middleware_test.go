// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httplimit

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
)

func TestMiddleware(t *testing.T) {
	t.Parallel()
	const maxBodySize = 1 << 20
	createHandler := func(next http.Handler) http.Handler { return Middleware(maxBodySize)(next) }
	readBodyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		assert.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	})
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("Request body within limit", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(make([]byte, maxBodySize-1)))
		rec := httptest.NewRecorder()
		createHandler(readBodyHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
	t.Run("Request body exactly at limit", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(make([]byte, maxBodySize)))
		rec := httptest.NewRecorder()
		createHandler(readBodyHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
	t.Run("Request body exceeds limit via Content-Length", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(make([]byte, maxBodySize+1)))
		rec := httptest.NewRecorder()
		createHandler(okHandler).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "Request Entity Too Large")
	})
	t.Run("MaxBytesReader converts handler 400 to 413 when limit exceeded", func(t *testing.T) {
		t.Parallel()
		largeArray := "["
		for i := 0; i < 100000; i++ {
			if i > 0 {
				largeArray += ","
			}
			largeArray += `{"key":"value"}`
		}
		largeArray += "]"
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", bytes.NewBuffer([]byte(largeArray)))
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
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", bytes.NewBuffer(make([]byte, maxBodySize)))
		rec := httptest.NewRecorder()
		validateHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/workloads", bytes.NewBuffer([]byte(`{"name":""}`)))
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

func TestMiddleware_NonPositiveLimitFallsBackToDefault(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		maxBytes int64
	}{{"zero", 0}, {"negative", -1}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer(make([]byte, DefaultMaxRequestBodySize+1)))
			rec := httptest.NewRecorder()
			Middleware(tt.maxBytes)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rec, req)
			assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		})
	}
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	setWriteDeadlineCalled bool
}

func (w *deadlineResponseWriter) SetWriteDeadline(time.Time) error {
	w.setWriteDeadlineCalled = true
	return nil
}

func TestBodySizeResponseWriter_UnwrapEnablesResponseController(t *testing.T) {
	t.Parallel()
	fake := &deadlineResponseWriter{ResponseWriter: httptest.NewRecorder()}
	wrapped := &bodySizeResponseWriter{ResponseWriter: fake, limitExceeded: new(bool)}
	err := http.NewResponseController(wrapped).SetWriteDeadline(time.Time{})
	require.NoError(t, err)
	assert.NotErrorIs(t, err, http.ErrNotSupported)
	assert.True(t, fake.setWriteDeadlineCalled, "expected SetWriteDeadline to reach the underlying writer via Unwrap")
}

type flushResponseWriter struct {
	http.ResponseWriter
	flushCalled bool
}

func (w *flushResponseWriter) Flush() { w.flushCalled = true }

type nonFlusherResponseWriter struct{}

func (nonFlusherResponseWriter) Header() http.Header         { return http.Header{} }
func (nonFlusherResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nonFlusherResponseWriter) WriteHeader(int)             {}

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

func TestIsRequestTooLarge(t *testing.T) {
	t.Parallel()
	err := http.MaxBytesError{Limit: 1}
	assert.True(t, IsRequestTooLarge(&err))
	assert.False(t, IsRequestTooLarge(io.EOF))
}
