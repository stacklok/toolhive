// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReadServerInfo_TCP(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "http://127.0.0.1:52341",
		PID:       12345,
		Nonce:     "test-nonce-tcp",
		StartedAt: time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC),
	}

	require.NoError(t, writeServerInfoTo(dir, info))

	got, err := readServerInfoFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, info.URL, got.URL)
	assert.Equal(t, info.PID, got.PID)
	assert.Equal(t, info.Nonce, got.Nonce)
	assert.True(t, info.StartedAt.Equal(got.StartedAt))
}

func TestWriteReadServerInfo_UnixSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "unix:///tmp/thv-test.sock",
		PID:       54321,
		Nonce:     "test-nonce-unix",
		StartedAt: time.Date(2026, 3, 23, 11, 0, 0, 0, time.UTC),
	}

	require.NoError(t, writeServerInfoTo(dir, info))

	got, err := readServerInfoFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, info.URL, got.URL)
	assert.Equal(t, info.PID, got.PID)
	assert.Equal(t, info.Nonce, got.Nonce)
}

// TestWriteReadServerInfo_NamedPipe pins the producer/consumer contract for
// npipe:// discovery URLs end to end. The individual pieces (socketURL emit,
// HTTPClientForURL dispatch, ParseNamedPipeURL parse) are covered in their
// own tests; this test asserts that an npipe URL survives the discovery
// file's JSON serialization round-trip without being mangled.
func TestWriteReadServerInfo_NamedPipe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "npipe://thv-api",
		PID:       99999,
		Nonce:     "test-nonce-pipe",
		StartedAt: time.Date(2026, 5, 7, 14, 0, 0, 0, time.UTC),
	}

	require.NoError(t, writeServerInfoTo(dir, info))

	got, err := readServerInfoFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, info.URL, got.URL)
	assert.Equal(t, info.PID, got.PID)
	assert.Equal(t, info.Nonce, got.Nonce)
}

func TestReadServerInfo_NotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := readServerInfoFrom(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveServerInfo_Exists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	require.NoError(t, removeServerInfoFrom(dir))

	_, err := readServerInfoFrom(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveServerInfo_NotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Should not error when file doesn't exist
	require.NoError(t, removeServerInfoFrom(dir))
}

func TestWriteServerInfo_FilePermissions(t *testing.T) {
	t.Parallel()
	// POSIX file modes are advisory on NTFS; Windows reports 0666 for
	// newly written files regardless of the mode passed to AtomicWriteFile.
	// Directory ACL coverage for Windows lives in permissions_windows_test.go.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	dir := t.TempDir()

	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	fi, err := os.Stat(filepath.Join(dir, "server.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(filePermissions), fi.Mode().Perm())
}

func TestWriteServerInfo_CreatesDirectoryWithCorrectPermissions(t *testing.T) {
	t.Parallel()
	// On Windows, writeServerInfoTo sets an explicit DACL instead of relying
	// on os.Chmod; see TestWriteServerInfo_WindowsDACL_NoOtherInteractiveUsers.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory modes are not meaningful on Windows; see DACL tests")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "nested", "server")

	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(dirPermissions), fi.Mode().Perm())
}

func TestWriteServerInfo_RejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a symlink at the target path
	target := filepath.Join(t.TempDir(), "evil.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "server.json")))

	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "nonce",
		StartedAt: time.Now().UTC(),
	}
	err := writeServerInfoTo(dir, info)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestReadServerInfo_RejectsSymlink(t *testing.T) {
	t.Parallel()

	// Write a valid server.json in a real directory.
	realDir := t.TempDir()
	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "real-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(realDir, info))

	// Create a second directory with a symlink named server.json that
	// points to the real file.
	symlinkDir := t.TempDir()
	realFile := filepath.Join(realDir, "server.json")
	symlinkFile := filepath.Join(symlinkDir, "server.json")
	require.NoError(t, os.Symlink(realFile, symlinkFile))

	_, err := readServerInfoFrom(symlinkDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestWriteServerInfo_TightensExistingDirPermissions(t *testing.T) {
	t.Parallel()
	// On Windows the equivalent "tighten existing" path is covered by
	// TestRestrictDiscoveryDirPermissions_ReplacesExistingLooseACL.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory modes are not meaningful on Windows; see DACL tests")
	}

	// Create a directory with deliberately too-loose permissions.
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0755))

	info := &ServerInfo{
		URL:       "http://127.0.0.1:8080",
		PID:       1,
		Nonce:     "tighten-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	// Verify the directory permissions were tightened to 0700.
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(dirPermissions), fi.Mode().Perm())
}

func TestWriteServerInfo_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	first := &ServerInfo{
		URL:   "http://127.0.0.1:8080",
		PID:   1,
		Nonce: "first",
	}
	require.NoError(t, writeServerInfoTo(dir, first))

	second := &ServerInfo{
		URL:   "http://127.0.0.1:9090",
		PID:   2,
		Nonce: "second",
	}
	require.NoError(t, writeServerInfoTo(dir, second))

	got, err := readServerInfoFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "second", got.Nonce)
	assert.Equal(t, "http://127.0.0.1:9090", got.URL)
}
