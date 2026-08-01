// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import "context"

//go:generate mockgen -destination=mocks/mock_lock_service.go -package=mocks -source=lock_service.go SkillLockService

// SkillLockService defines the interface for operations driven by a
// project's toolhive.lock.yaml. It is separate from [SkillService] because
// Sync and Upgrade operate over the whole lock file rather than a single
// named skill, per RFC THV-0080.
type SkillLockService interface {
	// Sync restores the project's installed skills to match its lock file.
	Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error)
	// Upgrade re-resolves each lock entry's source and installs newer content
	// when the resolved digest has changed.
	Upgrade(ctx context.Context, opts UpgradeOptions) (*UpgradeResult, error)
}
