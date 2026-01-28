// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       *RegistryError
		checkFunc func(*testing.T, *RegistryError)
	}{
		{
			name: "timeout error",
			err: &RegistryError{
				Type: RegistryTypeURL,
				URL:  "https://example.com",
				Err:  fmt.Errorf("%w: connection timeout", ErrRegistryTimeout),
			},
			checkFunc: func(t *testing.T, err *RegistryError) {
				t.Helper()
				assert.True(t, err.IsTimeout(), "should be a timeout error")
				assert.False(t, err.IsUnreachable(), "should not be an unreachable error")
				assert.False(t, err.IsValidationFailed(), "should not be a validation error")
			},
		},
		{
			name: "unreachable error",
			err: &RegistryError{
				Type: RegistryTypeAPI,
				URL:  "https://example.com",
				Err:  fmt.Errorf("%w: connection refused", ErrRegistryUnreachable),
			},
			checkFunc: func(t *testing.T, err *RegistryError) {
				t.Helper()
				assert.False(t, err.IsTimeout(), "should not be a timeout error")
				assert.True(t, err.IsUnreachable(), "should be an unreachable error")
				assert.False(t, err.IsValidationFailed(), "should not be a validation error")
			},
		},
		{
			name: "validation error",
			err: &RegistryError{
				Type: RegistryTypeURL,
				URL:  "https://example.com",
				Err:  fmt.Errorf("%w: invalid format", ErrRegistryValidationFailed),
			},
			checkFunc: func(t *testing.T, err *RegistryError) {
				t.Helper()
				assert.False(t, err.IsTimeout(), "should not be a timeout error")
				assert.False(t, err.IsUnreachable(), "should not be an unreachable error")
				assert.True(t, err.IsValidationFailed(), "should be a validation error")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.checkFunc(t, tt.err)
		})
	}
}

func TestRegistryErrorUnwrap(t *testing.T) {
	t.Parallel()

	innerErr := errors.New("inner error")
	regErr := &RegistryError{
		Type: RegistryTypeURL,
		URL:  "https://example.com",
		Err:  innerErr,
	}

	assert.True(t, errors.Is(regErr, innerErr), "should unwrap to inner error")
}

func TestClassifyNetworkError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		err           error
		expectedError error
	}{
		{
			name:          "nil error",
			err:           nil,
			expectedError: nil,
		},
		{
			name: "timeout error",
			err: &timeoutError{
				err: "connection timeout",
			},
			expectedError: ErrRegistryTimeout,
		},
		{
			name:          "context deadline exceeded",
			err:           context.DeadlineExceeded,
			expectedError: ErrRegistryTimeout,
		},
		{
			name:          "DNS error",
			err:           &net.DNSError{Err: "no such host", Name: "example.com"},
			expectedError: ErrRegistryUnreachable,
		},
		{
			name:          "connection refused",
			err:           errors.New("connection refused"),
			expectedError: ErrRegistryUnreachable,
		},
		{
			name:          "no route to host",
			err:           errors.New("no route to host"),
			expectedError: ErrRegistryUnreachable,
		},
		{
			name:          "network is unreachable",
			err:           errors.New("network is unreachable"),
			expectedError: ErrRegistryUnreachable,
		},
		{
			name:          "generic error",
			err:           errors.New("generic error"),
			expectedError: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := classifyNetworkError(tt.err)

			if tt.expectedError == nil {
				if tt.err == nil {
					assert.NoError(t, result)
				} else {
					assert.NotNil(t, result)
					assert.False(t, errors.Is(result, ErrRegistryTimeout))
					assert.False(t, errors.Is(result, ErrRegistryUnreachable))
					assert.False(t, errors.Is(result, ErrRegistryValidationFailed))
				}
			} else {
				assert.Error(t, result)
				assert.True(t, errors.Is(result, tt.expectedError))
			}
		})
	}
}

// timeoutError is a mock net.Error that implements the Timeout() method
type timeoutError struct {
	err string
}

func (e *timeoutError) Error() string { return e.err }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return false }

var _ net.Error = (*timeoutError)(nil)
