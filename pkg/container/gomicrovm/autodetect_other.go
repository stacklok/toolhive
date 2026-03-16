// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package gomicrovm

import "fmt"

// IsAvailable returns false on non-Linux platforms because go-microvm
// requires KVM, which is only available on Linux.
func IsAvailable() bool {
	return false
}

// FindRunnerPath always returns an error on non-Linux platforms.
func FindRunnerPath() (string, error) {
	return "", fmt.Errorf("go-microvm runtime requires Linux with KVM support")
}
