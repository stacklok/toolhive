// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package groups

import (
	"context"
	"fmt"
	"slices"
)

// AddPluginToGroup adds pluginName to the Plugins slice of the named group.
// Groups that do not exist return an error. Duplicate plugin names are skipped.
// Empty groupName is a no-op. The bool reports whether this call inserted the
// name (false when it was already a member or groupName is empty), so a later
// rollback can remove it only when this operation added it.
func AddPluginToGroup(ctx context.Context, mgr Manager, groupName string, pluginName string) (added bool, err error) {
	if groupName == "" {
		return false, nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return false, fmt.Errorf("getting group %q: %w", groupName, err)
	}

	if slices.Contains(group.Plugins, pluginName) {
		return false, nil
	}

	group.Plugins = append(group.Plugins, pluginName)
	if err := mgr.Update(ctx, group); err != nil {
		return false, fmt.Errorf("updating group %q: %w", groupName, err)
	}
	return true, nil
}

// RemovePluginFromGroup removes pluginName from the named group's Plugins
// slice. Missing membership is a no-op. Empty groupName is a no-op.
func RemovePluginFromGroup(ctx context.Context, mgr Manager, groupName string, pluginName string) error {
	if groupName == "" {
		return nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return fmt.Errorf("getting group %q: %w", groupName, err)
	}

	idx := slices.Index(group.Plugins, pluginName)
	if idx < 0 {
		return nil
	}
	group.Plugins = slices.Delete(group.Plugins, idx, idx+1)
	if err := mgr.Update(ctx, group); err != nil {
		return fmt.Errorf("updating group %q: %w", groupName, err)
	}
	return nil
}
