// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package errors

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
)

func TestErrorHandler(t *testing.T) {
	t.Parallel()

	t.Run("passes through successful response", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(w http.ResponseWriter, _ *http.Request) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
			return nil
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "success", rec.Body.String())
	})

	t.Run("converts 400 error to HTTP response with message", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return httperr.WithCode(
				fmt.Errorf("invalid input"),
				http.StatusBadRequest,
			)
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "invalid input")
	})

	t.Run("converts 404 error to HTTP response with message", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return httperr.WithCode(
				fmt.Errorf("resource not found"),
				http.StatusNotFound,
			)
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Contains(t, rec.Body.String(), "resource not found")
	})

	t.Run("converts 409 error to HTTP response with message", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return httperr.WithCode(
				fmt.Errorf("resource already exists"),
				http.StatusConflict,
			)
		})

		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "resource already exists")
	})

	t.Run("converts 500 error to generic HTTP response", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return httperr.WithCode(
				fmt.Errorf("sensitive database error details"),
				http.StatusInternalServerError,
			)
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		// Should NOT contain the sensitive error details
		require.False(t, strings.Contains(rec.Body.String(), "sensitive"))
		// Should contain generic message
		require.Contains(t, rec.Body.String(), "Internal Server Error")
	})

	t.Run("error without code defaults to 500 with generic message", func(t *testing.T) {
		t.Parallel()

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return errors.New("plain error without code")
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		// Should NOT contain the original error details
		require.False(t, strings.Contains(rec.Body.String(), "plain error"))
		// Should contain generic message
		require.Contains(t, rec.Body.String(), "Internal Server Error")
	})

	t.Run("handles wrapped error with code", func(t *testing.T) {
		t.Parallel()

		sentinelErr := httperr.WithCode(
			errors.New("not found"),
			http.StatusNotFound,
		)

		handler := ErrorHandler(func(_ http.ResponseWriter, _ *http.Request) error {
			return fmt.Errorf("workload lookup failed: %w", sentinelErr)
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Contains(t, rec.Body.String(), "workload lookup failed")
	})
}

func TestHandlerWithError_Type(t *testing.T) {
	t.Parallel()

	// Ensure HandlerWithError can be used as expected
	var handler HandlerWithError = func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)
		return nil
	}

	wrapped := ErrorHandler(handler)
	require.NotNil(t, wrapped)
}
