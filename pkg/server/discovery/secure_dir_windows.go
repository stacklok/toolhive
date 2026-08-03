// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

// discoveryDirsToSecure returns every directory in the discovery chain that
// receives an explicit protected DACL on Windows.
func discoveryDirsToSecure(base string) []string {
	return discoveryDirChain(base)
}
