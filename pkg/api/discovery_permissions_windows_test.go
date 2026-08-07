// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/stacklok/toolhive/pkg/server/discovery"
)

// TestWriteDiscoveryFile_RestrictsDirBeforeTrustingExistingFile pins the
// production ordering. writeDiscoveryFile must lock down the discovery chain
// before it acquires server.json.lock or calls Discover. A pre-planted file
// written while the chain was still loose must not be deleted during lockdown;
// startup fails closed when the planted record answers healthy.
//
//nolint:paralleltest // t.Setenv and xdg.Reload mutate process-wide state
func TestWriteDiscoveryFile_RestrictsDirBeforeTrustingExistingFile(t *testing.T) {
	base := t.TempDir()
	grantEveryone(t, base)

	t.Setenv("XDG_STATE_HOME", base)
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	// Guard against silently operating on the real %LOCALAPPDATA%: everything
	// below rewrites ACLs.
	require.Equal(t, base, xdg.StateHome, "XDG_STATE_HOME must redirect the discovery path")

	serverDir := filepath.Dir(discovery.FilePath())
	toolhiveDir := filepath.Dir(serverDir)
	require.Equal(t, base, filepath.Dir(toolhiveDir))

	// A server that answers /health with the planted nonce, so Discover
	// classifies the planted file as StateRunning.
	const plantedNonce = "planted-nonce"
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(discovery.NonceHeader, plantedNonce)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(healthSrv.Close)

	// Plant the file the way an attacker with write access to a loose
	// directory would: plain MkdirAll, so both directories inherit Everyone.
	require.NoError(t, os.MkdirAll(serverDir, 0700))
	planted, err := json.Marshal(&discovery.ServerInfo{
		URL:       healthSrv.URL,
		PID:       os.Getpid(),
		Nonce:     plantedNonce,
		StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(discovery.FilePath(), planted, 0600))
	require.Contains(t, strings.ToUpper(dirSDDL(t, serverDir)), "WD",
		"precondition: the planted directory must inherit Everyone (WD)")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	s := &Server{listener: listener, address: listener.Addr().String(), nonce: "our-nonce"}

	err = s.writeDiscoveryFile(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// The chain must already be locked down, on the leaf and on the
	// intermediate directory.
	require.NoError(t, discovery.ValidateRestrictedDiscoveryDACL(toolhiveDir))
	require.NoError(t, discovery.ValidateRestrictedDiscoveryDACL(serverDir))

	// The forged record must survive lockdown so a healthy predecessor is not erased.
	onDisk, err := os.ReadFile(discovery.FilePath())
	require.NoError(t, err)
	assert.Equal(t, string(planted), string(onDisk))
}

func grantEveryone(t *testing.T, path string) {
	t.Helper()
	// icacls is the product-path way to introduce a loose ACE; quoting keeps
	// PowerShell from expanding (OI)/(CI).
	out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:(OI)(CI)M").CombinedOutput()
	require.NoError(t, err, "icacls grant Everyone failed: %s", out)
}

func dirSDDL(t *testing.T, dir string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	return sd.String()
}
