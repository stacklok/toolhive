// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestWriteServerInfo_WindowsDACL_NoOtherInteractiveUsers asserts the
// acceptance criterion from #5217: after writeServerInfoTo, the discovery
// directory DACL grants access only to the current user and SYSTEM, and does
// not retain Everyone / Authenticated Users / other interactive-user ACEs
// that MkdirAll would otherwise inherit (and that os.Chmod cannot strip).
func TestWriteServerInfo_WindowsDACL_NoOtherInteractiveUsers(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "toolhive", "server")

	// Seed a deliberately loose ACL on the parent so newly created children
	// inherit Everyone. This models a shared / misconfigured LOCALAPPDATA
	// tree better than relying on whatever TempDir happens to carry.
	grantEveryone(t, parent)

	info := &ServerInfo{
		URL:       "npipe://thv-api",
		PID:       1,
		Nonce:     "dacl-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))

	assertDiscoveryDACLRestricted(t, dir)
}

// TestRestrictDiscoveryDirPermissions_ReplacesExistingLooseACL covers the
// sibling failure mode: the discovery directory already exists with a loose
// DACL (Everyone + inherited ACEs). restrictDiscoveryDirPermissions must
// replace that ACL rather than merge, and block further inheritance.
func TestRestrictDiscoveryDirPermissions_ReplacesExistingLooseACL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	grantEveryone(t, dir)

	before := discoveryDirSDDL(t, dir)
	require.Contains(t, strings.ToUpper(before), "WD", "precondition: Everyone (WD) must be present before restrict")

	require.NoError(t, restrictDiscoveryDirPermissions(dir))
	assertDiscoveryDACLRestricted(t, dir)
}

// TestRestrictDiscoveryDirPermissions_NewDirectory covers the create path
// (no pre-existing ACL to replace) so MkdirAll + restrict still lands a
// protected owner/SYSTEM-only DACL.
func TestRestrictDiscoveryDirPermissions_NewDirectory(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	grantEveryone(t, parent)
	dir := filepath.Join(parent, "fresh-server")
	require.NoError(t, os.MkdirAll(dir, dirPermissions))

	require.NoError(t, restrictDiscoveryDirPermissions(dir))
	assertDiscoveryDACLRestricted(t, dir)
}

func grantEveryone(t *testing.T, path string) {
	t.Helper()
	// icacls grants are the product-path way to introduce a loose ACE;
	// quoting keeps PowerShell from expanding (OI)/(CI).
	cmd := exec.Command("icacls", path, "/grant", "*S-1-1-0:(OI)(CI)M")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "icacls grant Everyone failed: %s", out)
}

func assertDiscoveryDACLRestricted(t *testing.T, dir string) {
	t.Helper()

	userSID, err := currentProcessUserSID()
	require.NoError(t, err)
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	authUsersSID, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	require.NoError(t, err)

	sd, err := windows.GetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	require.NoError(t, err)

	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED, "DACL must be protected against inheritance")

	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl)

	aces, err := allowACEsFromACL(dacl)
	require.NoError(t, err)

	var userSeen, systemSeen bool
	for _, ace := range aces {
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case userSID.Equals(sid):
			userSeen = true
		case systemSID.Equals(sid):
			systemSeen = true
		case everyoneSID.Equals(sid):
			t.Fatalf("DACL still grants Everyone (%s)", sid)
		case authUsersSID.Equals(sid):
			t.Fatalf("DACL still grants Authenticated Users (%s)", sid)
		default:
			// Administrators / package SIDs / other interactive users must
			// not remain after an explicit replace. Fail closed on anything
			// that is not the process user or SYSTEM.
			t.Fatalf("unexpected allow ACE for SID %s (want only current user + SYSTEM)", sid)
		}
	}
	assert.True(t, userSeen, "DACL must grant the current process user")
	assert.True(t, systemSeen, "DACL must grant SYSTEM")
}

func discoveryDirSDDL(t *testing.T, dir string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	require.NoError(t, err)
	return sd.String()
}

func allowACEsFromACL(acl *windows.ACL) ([]*windows.ACCESS_ALLOWED_ACE, error) {
	aces := make([]*windows.ACCESS_ALLOWED_ACE, 0, acl.AceCount)
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		aces = append(aces, ace)
	}
	return aces, nil
}
