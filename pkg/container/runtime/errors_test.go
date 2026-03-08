// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *ContainerError
		expected string
	}{
		{
			name: "message and container ID",
			err: &ContainerError{
				Err:         ErrContainerExited,
				ContainerID: "abc123",
				Message:     "exited with code 1",
			},
			expected: "container exited unexpectedly: exited with code 1 (container: abc123)",
		},
		{
			name: "message without container ID",
			err: &ContainerError{
				Err:     ErrContainerNotRunning,
				Message: "container is not running",
			},
			expected: "container not running: container is not running",
		},
		{
			name: "container ID without message",
			err: &ContainerError{
				Err:         ErrContainerRemoved,
				ContainerID: "def456",
			},
			expected: "container removed (container: def456)",
		},
		{
			name: "bare error only",
			err: &ContainerError{
				Err: ErrContainerNotFound,
			},
			expected: "container not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestContainerError_Unwrap(t *testing.T) {
	t.Parallel()

	underlying := ErrContainerExited
	ce := &ContainerError{
		Err:         underlying,
		ContainerID: "test",
		Message:     "some message",
	}

	// Unwrap should return the underlying error
	assert.Equal(t, underlying, ce.Unwrap())

	// errors.Is should work through Unwrap
	assert.True(t, errors.Is(ce, ErrContainerExited))
	assert.False(t, errors.Is(ce, ErrContainerNotFound))
}

func TestNewContainerError(t *testing.T) {
	t.Parallel()

	ce := NewContainerError(ErrContainerRemoved, "container-1", "was removed externally")

	require.NotNil(t, ce)
	assert.Equal(t, ErrContainerRemoved, ce.Err)
	assert.Equal(t, "container-1", ce.ContainerID)
	assert.Equal(t, "was removed externally", ce.Message)
}

func TestIsContainerNotFound(t *testing.T) {
	t.Parallel()

	t.Run("direct", func(t *testing.T) {
		t.Parallel()
		assert.True(t, IsContainerNotFound(ErrContainerNotFound))
	})

	t.Run("wrapped", func(t *testing.T) {
		t.Parallel()
		err := NewContainerError(ErrContainerNotFound, "cid", "not found")
		assert.True(t, IsContainerNotFound(err))
	})

	t.Run("other error", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsContainerNotFound(fmt.Errorf("different")))
	})

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsContainerNotFound(nil))
	})
}
