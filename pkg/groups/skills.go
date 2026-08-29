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
// Empty groupName is a no-op. The bool reports whether this call inserted the
// name (false when it was already a member or groupName is empty), so a later
// rollback can remove it only when this operation added it.
func AddSkillToGroup(ctx context.Context, mgr Manager, groupName string, skillName string) (added bool, err error) {
	if groupName == "" {
		return false, nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return false, fmt.Errorf("getting group %q: %w", groupName, err)
	}

	if slices.Contains(group.Skills, skillName) {
		return false, nil
	}

	group.Skills = append(group.Skills, skillName)
	if err := mgr.Update(ctx, group); err != nil {
		return false, fmt.Errorf("updating group %q: %w", groupName, err)
	}
	return true, nil
}

// RemoveSkillFromGroup removes skillName from the named group's Skills slice.
// Missing membership is a no-op. Empty groupName is a no-op.
func RemoveSkillFromGroup(ctx context.Context, mgr Manager, groupName string, skillName string) error {
	if groupName == "" {
		return nil
	}
	group, err := mgr.Get(ctx, groupName)
	if err != nil {
		return fmt.Errorf("getting group %q: %w", groupName, err)
	}

	idx := slices.Index(group.Skills, skillName)
	if idx < 0 {
		return nil
	}
	group.Skills = slices.Delete(group.Skills, idx, idx+1)
	if err := mgr.Update(ctx, group); err != nil {
		return fmt.Errorf("updating group %q: %w", group.Name, err)
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
