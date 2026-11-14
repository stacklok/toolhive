// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnixSocketURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"posix absolute", "/tmp/test.sock", "unix:///tmp/test.sock"},
		{"windows drive letter", `C:\path\thv.sock`, `unix:///C:%5Cpath%5Cthv.sock`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, UnixSocketURL(tt.address))
		})
	}
}

func TestUnixSocketURL_RoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		address string
	}{
		{"posix", "/tmp/test.sock"},
		{"drive letter", `C:\path\thv.sock`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUnixSocketPath(UnixSocketURL(tt.address))
			require.NoError(t, err)
			assert.Equal(t, tt.address, got)
		})
	}
}

func TestParseUnixSocketPath_FourSlashAlias(t *testing.T) {
	t.Parallel()
	// Older Windows ListenURL prepended / onto an already-absolute POSIX
	// path and emitted unix:////tmp/test.sock. New discovery files should
	// not do that, but the parser still has to accept the old form.
	got, err := ParseUnixSocketPath("unix:////tmp/test.sock")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/test.sock", got)
}

func TestParseUnixSocketPath_DriveLetterRejectsDotDot(t *testing.T) {
	t.Parallel()
	_, err := ParseUnixSocketPath(`unix:///C:%5Cpath%5C..%5CWindows%5Cthv.sock`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestParseUnixSocketPath_DriveRelativeRejected(t *testing.T) {
	t.Parallel()
	// Drive-relative paths (C:foo) are not absolute; filepath.IsAbs used to
	// reject them. Keep that contract so the health client cannot dial a
	// different socket from the process's current directory on C:.
	for _, raw := range []string{"unix:///C:relative.sock", "unix:///C:"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := ParseUnixSocketPath(raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "absolute")
		})
	}
}

func TestParseUnixSocketPathHelpers(t *testing.T) {
	t.Parallel()
	_, err := parseUnixSocketPath("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = parseUnixSocketPath("relative.sock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}
