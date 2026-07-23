// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictDiscoveryDirPermissions replaces the discovery directory DACL with
// an explicit ACL granting FILE-equivalent GenericAll only to the process
// user and SYSTEM, and marks the DACL protected so parent ACEs cannot
// re-inherit. os.Chmod is advisory on NTFS and does not strip inherited
// ACEs under %LOCALAPPDATA%, which is the gap that lets another interactive
// user rewrite server.json (for example to point at an attacker named pipe).
//
// Inheritance (OICI) is intentional: server.json and any future children
// pick up the same restriction instead of inheriting a looser parent ACL.
func restrictDiscoveryDirPermissions(dir string) error {
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
	return nil
}

func currentProcessUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tokenUser.User.Sid, nil
}
