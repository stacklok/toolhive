package errors

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithCode(t *testing.T) {
	t.Run("wraps error with code", func(t *testing.T) {
		baseErr := errors.New("test error")
		err := WithCode(baseErr, http.StatusNotFound)

		require.NotNil(t, err)

		coded, ok := err.(*CodedError)
		require.True(t, ok, "expected *CodedError, got %T", err)
		require.Equal(t, http.StatusNotFound, coded.HTTPCode())
		require.Equal(t, "test error", coded.Error())
	})

	t.Run("returns nil for nil error", func(t *testing.T) {
		err := WithCode(nil, http.StatusNotFound)
		require.Nil(t, err)
	})
}

func TestCode(t *testing.T) {
	t.Run("extracts code from CodedError", func(t *testing.T) {
		err := WithCode(errors.New("not found"), http.StatusNotFound)
		code := Code(err)
		require.Equal(t, http.StatusNotFound, code)
	})

	t.Run("returns 500 for error without code", func(t *testing.T) {
		err := errors.New("plain error")
		code := Code(err)
		require.Equal(t, http.StatusInternalServerError, code)
	})

	t.Run("returns 200 for nil error", func(t *testing.T) {
		code := Code(nil)
		require.Equal(t, http.StatusOK, code)
	})

	t.Run("extracts code from wrapped error", func(t *testing.T) {
		baseErr := WithCode(errors.New("not found"), http.StatusNotFound)
		wrappedErr := fmt.Errorf("outer context: %w", baseErr)
		code := Code(wrappedErr)
		require.Equal(t, http.StatusNotFound, code)
	})

	t.Run("extracts code from deeply wrapped error", func(t *testing.T) {
		baseErr := WithCode(errors.New("bad request"), http.StatusBadRequest)
		wrapped1 := fmt.Errorf("layer 1: %w", baseErr)
		wrapped2 := fmt.Errorf("layer 2: %w", wrapped1)
		wrapped3 := fmt.Errorf("layer 3: %w", wrapped2)
		code := Code(wrapped3)
		require.Equal(t, http.StatusBadRequest, code)
	})
}

func TestCodedError_Unwrap(t *testing.T) {
	t.Run("errors.Is works with wrapped error", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		err := WithCode(sentinel, http.StatusNotFound)
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("errors.Is works with double wrapped error", func(t *testing.T) {
		sentinel := errors.New("sentinel")
		coded := WithCode(sentinel, http.StatusNotFound)
		wrapped := fmt.Errorf("outer: %w", coded)
		require.ErrorIs(t, wrapped, sentinel)
	})

	t.Run("errors.As works with CodedError", func(t *testing.T) {
		err := WithCode(errors.New("test"), http.StatusBadRequest)
		wrapped := fmt.Errorf("wrapped: %w", err)

		var coded *CodedError
		require.ErrorAs(t, wrapped, &coded)
		require.Equal(t, http.StatusBadRequest, coded.HTTPCode())
	})
}

func TestHasCode(t *testing.T) {
	t.Run("returns true for CodedError", func(t *testing.T) {
		err := WithCode(errors.New("test"), http.StatusNotFound)
		require.True(t, HasCode(err))
	})

	t.Run("returns false for plain error", func(t *testing.T) {
		err := errors.New("plain error")
		require.False(t, HasCode(err))
	})

	t.Run("returns true for wrapped CodedError", func(t *testing.T) {
		err := WithCode(errors.New("test"), http.StatusNotFound)
		wrapped := fmt.Errorf("wrapped: %w", err)
		require.True(t, HasCode(wrapped))
	})
}

func TestNew(t *testing.T) {
	t.Run("creates error with message and code", func(t *testing.T) {
		err := New("custom error", http.StatusForbidden)
		require.Equal(t, "custom error", err.Error())
		require.Equal(t, http.StatusForbidden, Code(err))
	})
}

func TestCodedError_HTTPCode(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		expected int
	}{
		{"OK", http.StatusOK, http.StatusOK},
		{"BadRequest", http.StatusBadRequest, http.StatusBadRequest},
		{"NotFound", http.StatusNotFound, http.StatusNotFound},
		{"Conflict", http.StatusConflict, http.StatusConflict},
		{"InternalServerError", http.StatusInternalServerError, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WithCode(errors.New("test"), tt.code)
			coded := err.(*CodedError)
			require.Equal(t, tt.expected, coded.HTTPCode())
		})
	}
}
