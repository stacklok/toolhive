// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUnixSocketPath_Valid(t *testing.T) {
	t.Parallel()
	path, err := ParseUnixSocketPath("unix:///var/run/thv.sock")
	require.NoError(t, err)
	assert.Equal(t, "/var/run/thv.sock", path)
}

func TestParseUnixSocketPath_RelativePathRejected(t *testing.T) {
	t.Parallel()
	_, err := ParseUnixSocketPath("unix://relative/path.sock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestParseUnixSocketPath_DotDotRejected(t *testing.T) {
	t.Parallel()
	_, err := ParseUnixSocketPath("unix:///var/run/../etc/evil.sock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestParseUnixSocketPath_Empty(t *testing.T) {
	t.Parallel()
	_, err := ParseUnixSocketPath("unix://")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseNamedPipeURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		expect    string
		wantErr   bool
		errSubstr string
	}{
		{name: "valid", raw: "npipe://thv-api", expect: `\\.\pipe\thv-api`},
		{name: "valid with hyphen and digits", raw: "npipe://thv-api-1", expect: `\\.\pipe\thv-api-1`},
		{name: "valid with dot dot in name", raw: "npipe://my..api", expect: `\\.\pipe\my..api`},
		{name: "mixed-case scheme normalized", raw: "NPIPE://thv-api", expect: `\\.\pipe\thv-api`},
		{name: "mixed-case name normalized", raw: "npipe://Thv-API", expect: `\\.\pipe\thv-api`},
		{name: "missing scheme", raw: "thv-api", wantErr: true, errSubstr: "must start with npipe://"},
		{name: "wrong scheme", raw: "unix://thv-api", wantErr: true, errSubstr: "must start with npipe://"},
		{name: "empty name", raw: "npipe://", wantErr: true, errSubstr: "empty"},
		{name: "forward slash rejected", raw: "npipe://thv/api", wantErr: true, errSubstr: "form npipe://<name>"},
		{name: "backslash rejected by url.Parse", raw: `npipe://thv\api`, wantErr: true, errSubstr: "invalid"},
		{name: "with port rejected", raw: "npipe://thv-api:1234", wantErr: true, errSubstr: "form npipe://<name>"},
		{name: "with userinfo rejected", raw: "npipe://user:pass@thv-api", wantErr: true, errSubstr: "form npipe://<name>"},
		{name: "with query rejected", raw: "npipe://thv-api?x=1", wantErr: true, errSubstr: "form npipe://<name>"},
		{name: "with fragment rejected", raw: "npipe://thv-api#x", wantErr: true, errSubstr: "form npipe://<name>"},
		{name: "invalid charset rejected", raw: "npipe://thv$api", wantErr: true, errSubstr: "invalid characters"},
		{name: "reserved name CON rejected", raw: "npipe://CON", wantErr: true, errSubstr: "reserved Windows device name"},
		{name: "reserved name com1 rejected", raw: "npipe://com1", wantErr: true, errSubstr: "reserved Windows device name"},
		{
			name:      "name exceeds length cap",
			raw:       "npipe://" + strings.Repeat("a", maxNamedPipeNameLen+1),
			wantErr:   true,
			errSubstr: fmt.Sprintf("exceeds %d characters", maxNamedPipeNameLen),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseNamedPipeURL(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expect, got)
		})
	}
}

// TestHTTPClientForURL_SchemeDispatchCaseInsensitive pins that the dispatcher
// in HTTPClientForURL routes UNIX:// the same as unix:// because url.Parse
// lowercases the scheme. Without the migration this case fell through to the
// default "unsupported URL scheme" arm.
func TestHTTPClientForURL_SchemeDispatchCaseInsensitive(t *testing.T) {
	t.Parallel()
	_, baseURL, err := HTTPClientForURL("UNIX:///tmp/thv.sock")
	require.NoError(t, err)
	assert.Equal(t, "http://localhost", baseURL)
}

func TestCheckHealth_NamedPipe_Unsupported_OnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows guard test")
	}
	t.Parallel()
	err := CheckHealth(context.Background(), "npipe://thv-api", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Windows")
}

func TestCheckHealth_TCP_Success(t *testing.T) {
	t.Parallel()
	expectedNonce := "test-nonce-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, expectedNonce)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL, expectedNonce)
	require.NoError(t, err)
}

func TestCheckHealth_TCP_NonceMismatch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, "wrong-nonce")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL, "expected-nonce")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonce mismatch")
}

func TestCheckHealth_TCP_NoNonceCheck(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Empty expectedNonce skips nonce check
	err := CheckHealth(context.Background(), srv.URL, "")
	require.NoError(t, err)
}

func TestCheckHealth_UnixSocket_Success(t *testing.T) {
	t.Parallel()
	// Use os.MkdirTemp with a short name to stay under macOS's 104-char Unix socket path limit.
	// t.TempDir() produces paths like /private/var/folders/.../TestCheckHealth.../001/ which are too long.
	socketDir, err := os.MkdirTemp("", "thv-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	expectedNonce := "unix-nonce"
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(NonceHeader, expectedNonce)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	err = CheckHealth(context.Background(), "unix://"+socketPath, expectedNonce)
	require.NoError(t, err)
}

func TestCheckHealth_Unreachable(t *testing.T) {
	t.Parallel()
	err := CheckHealth(context.Background(), "http://127.0.0.1:1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check failed")
}

func TestCheckHealth_InvalidScheme(t *testing.T) {
	t.Parallel()
	err := CheckHealth(context.Background(), "ftp://localhost:21", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestCheckHealth_NonLoopbackRejected(t *testing.T) {
	t.Parallel()
	err := CheckHealth(context.Background(), "http://192.168.1.1:8080", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-loopback")
}

func TestCheckHealth_UnhealthyStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected health status")
}

func TestValidateLoopbackURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"IPv4 loopback", "http://127.0.0.1:8080", false},
		{"IPv6 loopback", "http://[::1]:8080", false},
		{"non-loopback", "http://192.168.1.1:8080", true},
		{"hostname", "http://example.com:8080", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateLoopbackURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCheckHealth_UnixSocket_NotFound(t *testing.T) {
	t.Parallel()
	socketPath := filepath.Join(os.TempDir(), "nonexistent-test.sock")
	err := CheckHealth(context.Background(), "unix://"+socketPath, "")
	require.Error(t, err)
}
