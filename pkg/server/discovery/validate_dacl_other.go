// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package discovery

import "fmt"

// ValidateRestrictedDiscoveryDACL is only meaningful on Windows.
func ValidateRestrictedDiscoveryDACL(dir string) error {
	return fmt.Errorf("ValidateRestrictedDiscoveryDACL is only supported on Windows (%s)", dir)
}
