// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package discovery

import (
	"fmt"
	"os"
)

// discoveryDirsToSecure returns every directory in the discovery chain that
// receives an explicit protected DACL on Windows.
func discoveryDirsToSecure(base string) []string {
	return discoveryDirChain(base)
}

func mkdirDiscoveryChain(chain []string) error {
	for _, dir := range chain {
		if err := os.MkdirAll(dir, dirPermissions); err != nil {
			return fmt.Errorf("failed to create discovery directory: %w", err)
		}
	}
	return nil
}
