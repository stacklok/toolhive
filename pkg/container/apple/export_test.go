// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"context"
)

// Constants exposed for external package tests in apple_test.
const (
	ContainerBinary = containerBinary
	SwVersBinary    = swVersBinary
)

// NewImageManagerWithRunner constructs an ImageManager backed by a mock CommandRunner.
func NewImageManagerWithRunner(runner CommandRunner) *ImageManager {
	return newImageManagerWithRunner(runner)
}

// ParseVersion exposes parseVersion for external package tests.
func ParseVersion(output string) (major, minor, patch int, ok bool) {
	return parseVersion(output)
}

// CheckAvailability exposes checkAvailability for external package tests.
func CheckAvailability(ctx context.Context, runner CommandRunner, goos, goarch string) error {
	return checkAvailability(ctx, runner, goos, goarch)
}

// CheckMacOSVersionForIsolation exposes checkMacOSVersionForIsolation for external package tests.
func CheckMacOSVersionForIsolation(ctx context.Context, runner CommandRunner) error {
	return checkMacOSVersionForIsolation(ctx, runner)
}
