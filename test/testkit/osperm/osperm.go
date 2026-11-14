// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package osperm holds test helpers for tests that depend on operating-system
// permission enforcement.
package osperm

import (
	"os"
	"testing"
)

// SkipIfRoot skips the test when it runs as root. Tests that force a write
// failure by removing write permission (chmod) cannot fail that write as root,
// because root bypasses file and directory permissions. On Windows os.Geteuid
// returns -1, so the test always runs.
func SkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: root bypasses file permissions, so a chmod-induced write failure cannot be forced")
	}
}
