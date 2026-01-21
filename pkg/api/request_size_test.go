// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
		// Create valid JSON that's larger than the limit to ensure decoder reads past limit
		// Use a large array of objects to make the decoder read the entire body
		largeArray := "["
		for i := 0; i < 100000; i++ {
			if i > 0 {
				largeArray += ","
			}
			largeArray += `{"key":"value"}`
		}
		largeArray += "]"

		oversizedBody := []byte(largeArray)
		body := bytes.NewBuffer(oversizedBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/test", body)

		// Lie about Content-Length to bypass early check
		req.ContentLength = maxBodySize - 1

		rec := httptest.NewRecorder()

		// Simulate a REAL handler that tries to decode JSON and returns 400 on error
		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data []map[string]interface{}
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

	t.Run("Validation errors return 400, not 413", func(t *testing.T) {
		t.Parallel()
		// This test verifies the bug fix: validation errors (400) should NOT be converted to 413
		// Create a small, valid JSON body (well within the limit)
		validationBody := []byte(`{"name":""}`)
		body := bytes.NewBuffer(validationBody)
		req := httptest.NewRequest(http.MethodPost, "/api/v1beta/workloads", body)
		rec := httptest.NewRecorder()

		// Simulate a handler that validates input and returns 400 for validation errors
		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				http.Error(w, "Failed to decode request", http.StatusBadRequest)
				return
			}
			// Validate the name field (simulate validation logic)
			name, ok := data["name"].(string)
			if !ok || name == "" {
				// Return 400 for validation error (empty name)
				http.Error(w, "Validation failed: name cannot be empty", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		handler := createHandler(nextHandler)
		handler.ServeHTTP(rec, req)

		// Validation errors should remain 400, NOT be converted to 413
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "Validation failed")
	})
}
