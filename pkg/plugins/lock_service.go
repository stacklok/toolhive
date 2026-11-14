// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import "context"

//go:generate mockgen -destination=mocks/mock_lock_service.go -package=mocks -source=lock_service.go PluginLockService

// PluginLockService defines the interface for operations driven by a
// project's toolhive.lock.yaml plugins: key. It is separate from
// [PluginService] because Sync and Upgrade operate over the whole lock
// file rather than a single named plugin, mirroring [skills.SkillLockService].
type PluginLockService interface {
	// Sync restores the project's installed plugins to match its lock file.
	Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error)
	// Upgrade re-resolves each plugins: lock entry's source and installs
	// newer content when the resolved digest has changed.
	Upgrade(ctx context.Context, opts UpgradeOptions) (*UpgradeResult, error)
}
