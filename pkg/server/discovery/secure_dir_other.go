// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package discovery

import (
	"fmt"
	"os"
)

const discoveryParentDirPermissions = 0750

// discoveryDirsToSecure returns only the server leaf on POSIX. The shared
// toolhive parent also holds runconfigs and toolhive.db created with 0750;
// chmodding it to 0700 would revoke group traversal for unrelated state.
func discoveryDirsToSecure(base string) []string {
	return []string{discoveryServerDir(base)}
}

// mkdirDiscoveryChain creates the discovery path without forcing 0700 on the
// shared intermediate toolhive directory. Parents are created first with
// discoveryParentDirPermissions so a later MkdirAll on the leaf does not
// apply dirPermissions up the chain.
func mkdirDiscoveryChain(chain []string) error {
	if len(chain) == 0 {
		return nil
	}
	for _, dir := range chain[:len(chain)-1] {
		if err := os.MkdirAll(dir, discoveryParentDirPermissions); err != nil {
			return fmt.Errorf("failed to create discovery directory: %w", err)
		}
	}
	leaf := chain[len(chain)-1]
	if err := os.MkdirAll(leaf, dirPermissions); err != nil {
		return fmt.Errorf("failed to create discovery directory: %w", err)
	}
	return nil
}
