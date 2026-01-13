package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestBodySizeLimitMiddleware(t *testing.T) {
	t.Parallel()
	// Define the limit (1MB)
	const maxBodySize = 1 << 20 // 1MB

	// Helper to create the middleware handler
	createHandler := func(next http.Handler) http.Handler {
		return requestBodySizeLimitMiddleware(maxBodySize)(next)
	}

	t.Run("Request body within limit", func(t *testing.T) {
		t.Parallel()
		// Create a request with a body smaller than the limit
		body := bytes.NewBuffer(make([]byte, maxBodySize-1))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		// Dummy handler that reads the body to trigger MaxBytesReader
		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := new(bytes.Buffer)
			_, err := buf.ReadFrom(r.Body)
			assert.NoError(t, err)
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Request body exactly at limit", func(t *testing.T) {
		t.Parallel()
		// Create a request with a body exactly at the limit
		body := bytes.NewBuffer(make([]byte, maxBodySize))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := new(bytes.Buffer)
			_, err := buf.ReadFrom(r.Body)
			assert.NoError(t, err)
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("Request body exceeds limit via Content-Length", func(t *testing.T) {
		t.Parallel()
		// Create a request with a body larger than the limit
		body := bytes.NewBuffer(make([]byte, maxBodySize+1))
		req := httptest.NewRequest(http.MethodPost, "/test", body)
		rec := httptest.NewRecorder()

		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "Request Entity Too Large")
	})

	t.Run("MaxBytesReader converts handler 400 to 413", func(t *testing.T) {
		t.Parallel()
		// Create oversized body
		oversizedBody := make([]byte, maxBodySize+100)
		body := bytes.NewBuffer(oversizedBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", body)

		// Lie about Content-Length to bypass early check
		req.ContentLength = maxBodySize - 1

		rec := httptest.NewRecorder()

		// Simulate a REAL handler that tries to decode JSON and returns 400 on error
		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data map[string]interface{}
			// This will fail because MaxBytesReader limits the read
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				// Real handlers return 400 Bad Request on decode errors
				http.Error(w, "Failed to decode request", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		// bodySizeResponseWriter should have converted 400 to 413
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("Empty request body succeeds", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBuffer([]byte{}))
		rec := httptest.NewRecorder()

		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
