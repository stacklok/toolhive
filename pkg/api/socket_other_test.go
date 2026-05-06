// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNamedPipeAddress(tt.address))
		})
	}
}
