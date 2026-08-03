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
	"regexp"
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
// written while the chain was still loose must be invalidated during that
// lockdown so a victim-owned server.json cannot keep redirecting clients after
// the DACL is repaired.
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
	require.NoError(t, err, "planted file must be invalidated before Discover trusts it")

	// The chain must already be locked down, on the leaf and on the
	// intermediate directory.
	assertRestrictedDACL(t, toolhiveDir)
	assertRestrictedDACL(t, serverDir)

	// The forged record must not survive the upgrade path.
	onDisk, err := os.ReadFile(discovery.FilePath())
	require.NoError(t, err)
	assert.NotEqual(t, string(planted), string(onDisk))
	assert.Contains(t, string(onDisk), "our-nonce")
}

func grantEveryone(t *testing.T, path string) {
	t.Helper()
	// icacls is the product-path way to introduce a loose ACE; quoting keeps
	// PowerShell from expanding (OI)/(CI).
	out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:(OI)(CI)M").CombinedOutput()
	require.NoError(t, err, "icacls grant Everyone failed: %s", out)
}

// aceTrusteePattern captures the trustee of each ACE in an SDDL DACL, which is
// the last of the six semicolon-separated ACE fields.
var aceTrusteePattern = regexp.MustCompile(`\(([^)]*)\)`)

// assertRestrictedDACL asserts dir carries a protected DACL that grants nobody
// but SYSTEM and the process user. The per-ACE permission checks live in the
// discovery package's own Windows tests; this is the pkg/api view of the same
// contract.
func assertRestrictedDACL(t *testing.T, dir string) {
	t.Helper()

	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	allowed := []string{"SY", tokenUser.User.Sid.String()}

	sddl := dirSDDL(t, dir)
	assert.Contains(t, sddl, "D:P", "DACL of %s must be protected against inheritance: %s", dir, sddl)
	for _, ace := range aceTrusteePattern.FindAllStringSubmatch(sddl, -1) {
		fields := strings.Split(ace[1], ";")
		trustee := fields[len(fields)-1]
		assert.Contains(t, allowed, trustee,
			"DACL of %s must grant only SYSTEM and the process user, found %s: %s", dir, trustee, sddl)
	}
}

func dirSDDL(t *testing.T, dir string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	return sd.String()
}
