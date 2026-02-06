// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fileutils provides file operation utilities including atomic writes
// and path validation for security.
package fileutils

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/workloads/types"
)

// ValidateWorkloadNameForPath validates a workload name to prevent path traversal attacks.
// It ensures the name is safe for use in file path construction by checking:
// - Path traversal patterns (..)
// - Absolute paths
// - Path separators (/, \)
// - Command injection patterns
// - Null bytes
// - Invalid characters (only alphanumeric, dots, hyphens, underscores allowed)
// - Length limits
//
// This function delegates to types.ValidateWorkloadName which performs comprehensive
// validation including filepath.Clean normalization and filepath.Rel path traversal checks.
//
// Returns nil if the workload name is safe for path construction, or an error describing
// the validation failure.
func ValidateWorkloadNameForPath(workloadName string) error {
	// The types.ValidateWorkloadName function already performs comprehensive validation:
	// - Empty check
	// - Null bytes detection
	// - Path normalization (filepath.Clean)
	// - Path traversal detection (filepath.Rel to check for ".." escapes)
	// - Absolute path rejection
	// - Command injection pattern detection
	// - Character validation (only [a-zA-Z0-9._-] allowed, which excludes / and \)
	// - Length limits (max 100 characters)
	//
	// This provides defense-in-depth against path traversal attacks.
	if err := types.ValidateWorkloadName(workloadName); err != nil {
		return fmt.Errorf("invalid workload name for path construction: %w", err)
	}

	return nil
}
