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
// Empty groupName is a no-op.
func AddPluginToGroup(ctx context.Context, mgr Manager, groupName string, pluginName string) error {
	if groupName == "" {
		return nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return fmt.Errorf("getting group %q: %w", groupName, err)
	}

	if slices.Contains(group.Plugins, pluginName) {
		return nil
	}

	group.Plugins = append(group.Plugins, pluginName)
	if err := mgr.Update(ctx, group); err != nil {
		return fmt.Errorf("updating group %q: %w", groupName, err)
	}
	return nil
}

// RemovePluginFromAllGroups removes pluginName from every group that references it.
// It is a no-op when the plugin is not found in any group.
func RemovePluginFromAllGroups(ctx context.Context, mgr Manager, pluginName string) error {
	allGroups, err := mgr.List(ctx)
	if err != nil {
		return fmt.Errorf("listing groups: %w", err)
	}

	for _, group := range allGroups {
		modified := false
		for i, p := range group.Plugins {
			if p == pluginName {
				group.Plugins = append(group.Plugins[:i], group.Plugins[i+1:]...)
				modified = true
				break
			}
		}
		if modified {
			if err := mgr.Update(ctx, group); err != nil {
				return fmt.Errorf("updating group %q: %w", group.Name, err)
			}
		}
	}
	return nil
}
