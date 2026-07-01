// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
)

// droppedComponents returns the component types declared in the inventory that
// are not present in the supported set.
func droppedComponents(inv plugins.ComponentInventory, supported []plugins.ComponentType) []plugins.ComponentType {
	if inv == nil {
		return nil
	}
	set := make(map[plugins.ComponentType]struct{}, len(supported))
	for _, ct := range supported {
		set[ct] = struct{}{}
	}
	var out []plugins.ComponentType
	for key := range inv {
		ct := plugins.ComponentType(key)
		if _, ok := set[ct]; !ok {
			out = append(out, ct)
		}
	}
	return out
}

// cleanupAfterRemove walks up from dir's parent removing empty directories,
// stopping at the project root (project scope) or home dir (user scope).
// homeDir is taken as a parameter (not os.UserHomeDir) so tests can inject a
// temp home without touching the host filesystem.
func cleanupAfterRemove(dir string, scope plugins.Scope, projectRoot, homeDir string) {
	stopAt := projectRoot
	if scope == plugins.ScopeUser {
		stopAt = homeDir
	}
	if stopAt != "" {
		skills.RemoveEmptyParents(filepath.Dir(dir), stopAt)
	}
}
