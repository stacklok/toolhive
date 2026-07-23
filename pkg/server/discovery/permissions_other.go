// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package discovery

import (
	"fmt"
	"os"
)

// restrictDiscoveryDirPermissions tightens POSIX mode bits on the discovery
// directory. On non-Windows platforms this is the Chmod that previously lived
// inline in writeServerInfoTo.
func restrictDiscoveryDirPermissions(dir string) error {
	if err := os.Chmod(dir, dirPermissions); err != nil {
		return fmt.Errorf("failed to set discovery directory permissions: %w", err)
	}
	return nil
}
