// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ancestor invariant on Windows: ToolHive protects and owner-checks every
// directory it creates for the discovery file (%LOCALAPPDATA%\toolhive and
// %LOCALAPPDATA%\toolhive\server). Above that it relies on the OS default ACL
// of %LOCALAPPDATA%, which grants the user, SYSTEM, and Administrators only.
// If another account does hold delete-child there, it can rename the chain
// aside and pre-create its own; the owner check below is what turns that into
// a startup failure instead of silent trust, because a standard user cannot
// create a directory owned by the ToolHive user, SYSTEM, or Administrators.

// restrictDiscoveryDirPermissions locks down one directory in the discovery
// chain: it fails closed unless the directory is owned by an account we trust,
// then replaces the DACL with an explicit ACL granting FILE-equivalent
// GenericAll only to the process user and SYSTEM, marked protected so parent
// ACEs cannot re-inherit. os.Chmod is advisory on NTFS and does not strip
// inherited ACEs under %LOCALAPPDATA%, which is the gap that lets another
// interactive user rewrite server.json (for example to point at a named pipe
// of their own).
//
// Inheritance (OICI) is intentional: server.json and any future children pick
// up the same restriction instead of inheriting a looser parent ACL.
func restrictDiscoveryDirPermissions(dir string) error {
	trusted, err := trustedOwnerSIDs()
	if err != nil {
		return err
	}
	return restrictDiscoveryDir(dir, trusted)
}

// discoveryDirPermissionsLoose reports whether dir carries an unprotected DACL
// or grants access to accounts outside the process user and SYSTEM.
func discoveryDirPermissionsLoose(dir string) (bool, error) {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat discovery directory: %w", err)
	}

	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("failed to read discovery directory DACL: %w", err)
	}

	control, _, err := sd.Control()
	if err != nil {
		return false, fmt.Errorf("failed to read discovery directory DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return true, nil
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("failed to read discovery directory DACL entries: %w", err)
	}
	if dacl == nil {
		return true, nil
	}

	userSID, err := currentProcessUserSID()
	if err != nil {
		return false, err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, err
	}

	aces, err := allowACEsFromACL(dacl)
	if err != nil {
		return false, err
	}
	for _, ace := range aces {
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if userSID.Equals(sid) || systemSID.Equals(sid) {
			continue
		}
		return true, nil
	}
	return false, nil
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

// restrictDiscoveryDir is the injectable core of
// restrictDiscoveryDirPermissions. Tests pass a trustedOwners set that
// excludes the real owner to exercise the fail-closed path without a second
// user account.
func restrictDiscoveryDir(dir string, trustedOwners []*windows.SID) error {
	// Check ownership before writing the DACL, not after. Owners keep
	// WRITE_DAC implicitly, so a DACL we set on a directory somebody else owns
	// is cosmetic: that owner can make it permissive again at any time.
	if err := checkOwner(dir, "directory", trustedOwners); err != nil {
		return err
	}

	userSID, err := currentProcessUserSID()
	if err != nil {
		return fmt.Errorf("failed to resolve current user SID: %w", err)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("failed to resolve SYSTEM SID: %w", err)
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(userSID),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to build discovery directory DACL: %w", err)
	}

	if err := windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("failed to set discovery directory DACL: %w", err)
	}

	// Re-check the owner before reporting the lockdown as complete. A
	// pre-creating owner that raced the DACL write could have taken the
	// directory back in between, and the caller is about to trust the contents.
	return checkOwner(dir, "directory", trustedOwners)
}

// validateDiscoveryFileOwner rejects a discovery file owned by an account
// outside the trusted set. The directory lockdown stops new writes but leaves
// the ACL of a file planted while the directory was loose untouched, so the
// file needs its own check before its URL and nonce are believed.
func validateDiscoveryFileOwner(path string) error {
	trusted, err := trustedOwnerSIDs()
	if err != nil {
		return err
	}
	return validateFileOwner(path, trusted)
}

// validateFileOwner is the injectable core of validateDiscoveryFileOwner.
func validateFileOwner(path string, trustedOwners []*windows.SID) error {
	if _, err := os.Lstat(path); err != nil {
		// A missing file is not a trust decision; the caller's own
		// os.ErrNotExist handling covers it.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("failed to stat discovery file: %w", err)
	}
	return checkOwner(path, "file", trustedOwners)
}

// checkOwner fails closed unless path is owned by one of trustedOwners.
func checkOwner(path string, kind string, trustedOwners []*windows.SID) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("failed to read discovery %s owner for %s: %w", kind, path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("failed to resolve discovery %s owner for %s: %w", kind, path, err)
	}
	for _, trustedOwner := range trustedOwners {
		if trustedOwner.Equals(owner) {
			return nil
		}
	}
	return fmt.Errorf(
		"refusing to trust discovery %s %s: it is owned by %s, not the current user, SYSTEM, or Administrators; "+
			"remove it (or restore its ownership) and retry",
		kind, path, owner,
	)
}

// trustedOwnerSIDs returns the accounts allowed to own the discovery directory
// chain and the discovery file: the process user, SYSTEM, and Administrators.
//
// Administrators is in the set because a directory created by an elevated
// `thv serve` is owned by Administrators rather than by the user, and because
// a local administrator is outside this threat model (they can take ownership
// of anything). It does not weaken the check against the attacker this fix is
// about: a standard user cannot make SYSTEM or Administrators the owner of a
// directory they create, so a pre-created hostile directory still fails.
func trustedOwnerSIDs() ([]*windows.SID, error) {
	userSID, err := currentProcessUserSID()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve current user SID: %w", err)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve SYSTEM SID: %w", err)
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve Administrators SID: %w", err)
	}
	return []*windows.SID{userSID, systemSID, adminsSID}, nil
}

func currentProcessUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tokenUser.User.Sid, nil
}
