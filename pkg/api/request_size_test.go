package api

import (
	"bytes"
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

	t.Run("MaxBytesReader enforces limit when Content-Length is misleading", func(t *testing.T) {
		t.Parallel()
		// Create a request where Content-Length is set incorrectly to test the MaxBytesReader safety net
		oversizedBody := make([]byte, maxBodySize+100)
		body := bytes.NewBuffer(oversizedBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", body)

		// Manually set Content-Length to be within limit to bypass early check
		req.ContentLength = maxBodySize - 1

		rec := httptest.NewRecorder()

		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try to read the entire body - MaxBytesReader should return an error
			buf := new(bytes.Buffer)
			_, err := buf.ReadFrom(r.Body)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}
