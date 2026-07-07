// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "context"

//go:generate mockgen -destination=mocks/mock_lock_service.go -package=mocks -source=lock_service.go SkillLockService

// SkillLockService defines lock-file operations for project-scoped skills.
type SkillLockService interface {
	// Sync installs the exact name/digest pinned in the project's lock file
	// for every entry, restoring drifted or missing skills.
	Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error)
	// Upgrade re-resolves each lock file entry's original source and, when the
	// resolved digest differs from the pinned one, installs the new content
	// and updates the lock file entry.
	Upgrade(ctx context.Context, opts UpgradeOptions) (*UpgradeResult, error)
}
