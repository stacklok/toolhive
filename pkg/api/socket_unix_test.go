// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketURL_Unix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "unix:///tmp/test.sock", socketURL("/tmp/test.sock"))
}

func TestIsNamedPipeAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address string
		want    bool
	}{
		{"plain socket", "/tmp/thv.sock", false},
		{"named pipe", `\\.\pipe\thv-api`, true},
		{"named pipe mixed case", `\\.\Pipe\thv-api`, true},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNamedPipeAddress(tt.address))
		})
	}
}

// TestCreateListener_NamedPipe_Unsupported asserts that createListener rejects
// pipe addresses on non-Windows up front, mirroring the dialer-side guard
// covered by TestCheckHealth_NamedPipe_Unsupported_OnNonWindows.
func TestCreateListener_NamedPipe_Unsupported(t *testing.T) {
	t.Parallel()
	_, _, err := createListener(`\\.\pipe\thv-api`, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supported on Windows")
}
