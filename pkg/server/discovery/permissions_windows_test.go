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

// TestEnsureSecureDirIn_ProtectsIntermediateToolhiveDir covers the ancestor
// replacement path. Restricting only the server leaf leaves the intermediate
// toolhive directory with inherited Modify, which carries DELETE on that
// object: another interactive user can rename toolhive aside, recreate
// toolhive\server with an ACL of their own, and the protected leaf is bypassed
// because nothing reads it any more. Both directories in the chain must end up
// protected.
func TestEnsureSecureDirIn_ProtectsIntermediateToolhiveDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	grantEveryone(t, base)

	chain := discoveryDirChain(base)
	require.Len(t, chain, 2)
	toolhiveDir, serverDir := chain[0], chain[1]

	// Model the state an earlier ToolHive left behind: both directories exist
	// already, created with plain MkdirAll, so both inherited Everyone.
	require.NoError(t, os.MkdirAll(serverDir, dirPermissions))
	require.Contains(t, strings.ToUpper(discoveryDirSDDL(t, toolhiveDir)), "WD",
		"precondition: intermediate toolhive dir must inherit Everyone (WD)")

	require.NoError(t, ensureSecureDirIn(base))

	assertDiscoveryDACLRestricted(t, toolhiveDir)
	assertDiscoveryDACLRestricted(t, serverDir)
}

// TestEnsureSecureDirIn_InvalidatesForgedDiscoveryFile covers the upgrade path
// where another user truncated and rewrote a victim-owned server.json while
// the directory chain was still loose. Tightening the DACL is not enough:
// ownership stayed with the victim, so the planted URL would still be trusted
// unless the record is invalidated.
func TestEnsureSecureDirIn_InvalidatesForgedDiscoveryFile(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	grantEveryone(t, base)

	chain := discoveryDirChain(base)
	serverDir := chain[len(chain)-1]
	require.NoError(t, os.MkdirAll(serverDir, 0700))

	planted := []byte(`{"url":"npipe://attacker","pid":1,"nonce":"forged","started_at":"2026-08-03T00:00:00Z"}`)
	require.NoError(t, os.WriteFile(filepath.Join(serverDir, "server.json"), planted, 0600))
	require.Contains(t, strings.ToUpper(discoveryDirSDDL(t, serverDir)), "WD",
		"precondition: server dir must inherit Everyone (WD)")

	require.NoError(t, ensureSecureDirIn(base))

	assertDiscoveryDACLRestricted(t, chain[0])
	assertDiscoveryDACLRestricted(t, serverDir)
	_, err := os.Stat(filepath.Join(serverDir, "server.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestRestrictDiscoveryDir_FailsClosedOnUntrustedOwner covers hostile
// ownership. Replacing the DACL is not enough when another account owns the
// directory: owners keep WRITE_DAC implicitly and can make the ACL permissive
// again, so the lockdown must fail rather than report success.
//
// Creating a directory owned by a different account needs a second user or
// SeRestorePrivilege, neither of which a unit test can assume, so the trusted
// owner set is injected instead: the directory is genuinely owned by the test
// user and the set deliberately does not include them.
func TestRestrictDiscoveryDir_FailsClosedOnUntrustedOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	grantEveryone(t, dir)

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)

	err = restrictDiscoveryDir(dir, []*windows.SID{systemSID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to trust discovery directory")

	// The DACL must be left untouched: a half-applied lockdown on a directory
	// somebody else owns would look secure to an operator reading icacls.
	assert.Contains(t, strings.ToUpper(discoveryDirSDDL(t, dir)), "WD",
		"loose ACE must survive: ownership is checked before the DACL is replaced")
}

// TestRestrictDiscoveryDir_AcceptsCurrentUserOwner is the positive half of the
// ownership check: the real trusted set must accept a directory the process
// created itself, so the fail-closed path above cannot pass by rejecting
// everything.
func TestRestrictDiscoveryDir_AcceptsCurrentUserOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	trusted, err := trustedOwnerSIDs()
	require.NoError(t, err)

	require.NoError(t, restrictDiscoveryDir(dir, trusted))
	assertDiscoveryDACLRestricted(t, dir)
}

// TestValidateFileOwner_RejectsUntrustedOwner covers the read path. The
// directory lockdown stops new writes but does not rewrite the ACL of a
// server.json planted while the directory was still loose, so the file's own
// owner decides whether its URL and nonce can be trusted.
func TestValidateFileOwner_RejectsUntrustedOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	info := &ServerInfo{
		URL:       "npipe://attacker-pipe",
		PID:       1,
		Nonce:     "planted-nonce",
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, writeServerInfoTo(dir, info))
	path := filepath.Join(dir, "server.json")

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	require.NoError(t, err)

	err = validateFileOwner(path, []*windows.SID{systemSID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to trust discovery file")

	// A file the process owns stays readable, and a missing file is not a
	// trust decision (readServerInfoFrom must still return os.ErrNotExist).
	trusted, err := trustedOwnerSIDs()
	require.NoError(t, err)
	assert.NoError(t, validateFileOwner(path, trusted))
	assert.NoError(t, validateFileOwner(filepath.Join(dir, "absent.json"), trusted))

	got, err := readServerInfoFrom(dir)
	require.NoError(t, err)
	assert.Equal(t, "planted-nonce", got.Nonce)
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
