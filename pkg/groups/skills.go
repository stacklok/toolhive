// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package groups

import (
	"context"
	"fmt"
	"slices"
)

// AddSkillToGroup adds skillName to the Skills slice of the named group.
// Groups that do not exist return an error. Duplicate skill names are skipped.
// Empty groupName is a no-op.
func AddSkillToGroup(ctx context.Context, mgr Manager, groupName string, skillName string) error {
	if groupName == "" {
		return nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return fmt.Errorf("getting group %q: %w", groupName, err)
	}

	if slices.Contains(group.Skills, skillName) {
		return nil
	}

	group.Skills = append(group.Skills, skillName)
	if err := mgr.Update(ctx, group); err != nil {
		return fmt.Errorf("updating group %q: %w", groupName, err)
	}
	return nil
}

// RemoveSkillFromAllGroups removes skillName from every group that references it.
// It is a no-op when the skill is not found in any group.
func RemoveSkillFromAllGroups(ctx context.Context, mgr Manager, skillName string) error {
	allGroups, err := mgr.List(ctx)
	if err != nil {
		return fmt.Errorf("listing groups: %w", err)
	}

	for _, group := range allGroups {
		modified := false
		for i, s := range group.Skills {
			if s == skillName {
				group.Skills = append(group.Skills[:i], group.Skills[i+1:]...)
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
