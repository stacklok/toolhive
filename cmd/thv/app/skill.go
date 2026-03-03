// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/skillsvc"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
)

// TODO: Remove Hidden flag when skills feature is ready for release.
var skillCmd = &cobra.Command{
	Use:    "skill",
	Short:  "Manage skills",
	Long:   `The skill command provides subcommands to manage skills.`,
	Hidden: true,
}

// newSkillService creates a SkillService configured for CLI use.
// It opens the default SQLite skill store and wires a groups.Manager.
// The caller must close the returned io.Closer when done.
func newSkillService() (skills.SkillService, io.Closer, error) {
	store, err := sqlite.NewDefaultSkillStore()
	if err != nil {
		return nil, nil, fmt.Errorf("opening skill store: %w", err)
	}

	groupMgr, err := groups.NewManager()
	if err != nil {
		_ = store.Close()
		return nil, nil, fmt.Errorf("creating group manager: %w", err)
	}

	svc := skillsvc.New(store, skillsvc.WithGroupManager(groupMgr))
	return svc, store, nil
}
