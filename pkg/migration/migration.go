// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package migration handles any migrations needed to maintain compatibility
package migration

import (
	"context"
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive/pkg/groups"
)

// ensureDefaultGroupOnce ensures the default group check only runs once per process
var ensureDefaultGroupOnce sync.Once

// EnsureDefaultGroupExists ensures the default group exists, creating it if necessary.
// This is called at application startup for fresh installs and is a no-op when
// the group already exists (e.g. after a previous migration or existing setup).
func EnsureDefaultGroupExists() {
	ensureDefaultGroupOnce.Do(func() {
		if err := ensureDefaultGroupExists(context.Background()); err != nil {
			slog.Error("failed to ensure default group exists", "error", err)
			return
		}
	})
}

func ensureDefaultGroupExists(ctx context.Context) error {
	groupManager, err := groups.NewManager()
	if err != nil {
		return err
	}

	exists, err := groupManager.Exists(ctx, groups.DefaultGroupName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	slog.Debug("creating default group", "name", groups.DefaultGroupName)
	return groupManager.Create(ctx, groups.DefaultGroupName)
}
