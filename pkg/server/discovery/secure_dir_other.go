// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package discovery

// discoveryDirsToSecure returns only the server leaf on POSIX. The shared
// toolhive parent also holds runconfigs and toolhive.db created with 0750;
// chmodding it to 0700 would revoke group traversal for unrelated state.
func discoveryDirsToSecure(base string) []string {
	return []string{discoveryServerDir(base)}
}
