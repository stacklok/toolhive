package mcpregistrystatus

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "normal message",
			err: &Error{
				Err:             errors.New("underlying error"),
				Message:         "custom error message",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: "custom error message",
		},
		{
			name: "empty message",
			err: &Error{
				Err:             errors.New("underlying error"),
				Message:         "",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: "",
		},
		{
			name: "message with special characters",
			err: &Error{
				Err:             errors.New("underlying error"),
				Message:         "Error: 50% of deployments failed\nRetry needed",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: "Error: 50% of deployments failed\nRetry needed",
		},
		{
			name: "nil underlying error",
			err: &Error{
				Err:             nil,
				Message:         "custom message without underlying error",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: "custom message without underlying error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.err.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *Error
		expected error
	}{
		{
			name: "normal underlying error",
			err: &Error{
				Err:             errors.New("underlying error"),
				Message:         "custom error message",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: errors.New("underlying error"),
		},
		{
			name: "nil underlying error",
			err: &Error{
				Err:             nil,
				Message:         "custom error message",
				ConditionType:   "TestCondition",
				ConditionReason: "TestReason",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.err.Unwrap()
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected.Error(), result.Error())
			}
		})
	}
}

func TestError_Interface(t *testing.T) {
	t.Parallel()

	// Test that Error implements the error interface
	var _ error = &Error{}

	// Test error chaining with errors.Is and errors.As
	originalErr := errors.New("original error")
	wrappedErr := &Error{
		Err:             originalErr,
		Message:         "wrapped error",
		ConditionType:   "TestCondition",
		ConditionReason: "TestReason",
	}

	// Test errors.Is
	assert.True(t, errors.Is(wrappedErr, originalErr))

	// Test errors.As
	var targetErr *Error
	assert.True(t, errors.As(wrappedErr, &targetErr))
	assert.Equal(t, "wrapped error", targetErr.Message)
	assert.Equal(t, "TestCondition", targetErr.ConditionType)
	assert.Equal(t, "TestReason", targetErr.ConditionReason)
}

func TestError_Fields(t *testing.T) {
	t.Parallel()

	originalErr := errors.New("original error")
	err := &Error{
		Err:             originalErr,
		Message:         "custom message",
		ConditionType:   "SyncFailed",
		ConditionReason: "NetworkError",
	}

	// Test that all fields are accessible and correct
	assert.Equal(t, originalErr, err.Err)
	assert.Equal(t, "custom message", err.Message)
	assert.Equal(t, "SyncFailed", err.ConditionType)
	assert.Equal(t, "NetworkError", err.ConditionReason)
}

func TestError_ZeroValue(t *testing.T) {
	t.Parallel()

	// Test zero value behavior
	var err Error

	assert.Equal(t, "", err.Error())
	assert.Nil(t, err.Unwrap())
	assert.Equal(t, "", err.ConditionType)
	assert.Equal(t, "", err.ConditionReason)
}
