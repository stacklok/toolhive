// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package migration handles any migrations needed to maintain compatibility
package migration

import (
	"context"
	"errors"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
)

// EnsureDefaultGroupExists ensures the default group exists, creating it if necessary.
// This is called at application startup for fresh installs and is a no-op when
// the group already exists (e.g. after a previous migration or existing setup).
// In Kubernetes environments this is always a no-op: MCPGroup CRDs are
// operator/user-managed resources and the caller's service account may not
// have create permission on them.
// When multiple processes race to create the group, the loser's conflict
// error is treated as success because the desired end state (group exists) holds.
func EnsureDefaultGroupExists() error {
	if runtime.IsKubernetesRuntime() {
		return nil
	}
	return ensureDefaultGroupExists(context.Background())
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
	if err := groupManager.Create(ctx, groups.DefaultGroupName); err != nil {
		// Another process may have won the creation race; the group now
		// exists, which is the desired end state.
		if errors.Is(err, groups.ErrGroupAlreadyExists) {
			return nil
		}
		return err
	}
	return nil
}
