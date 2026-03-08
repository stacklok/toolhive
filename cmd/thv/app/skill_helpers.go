// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
	skillclient "github.com/stacklok/toolhive/pkg/skills/client"
)

// newSkillClient creates a new Skills API HTTP client using default settings.
func newSkillClient() *skillclient.Client {
	return skillclient.NewDefaultClient()
}

// completeSkillNames provides shell completion for installed skill names.
func completeSkillNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	c := newSkillClient()
	installed, err := c.List(cmd.Context(), skills.ListOptions{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	names := make([]string, 0, len(installed))
	for _, s := range installed {
		names = append(names, s.Metadata.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// formatSkillError wraps an error with contextual information. If the
// underlying cause is ErrServerUnreachable it appends a helpful hint.
func formatSkillError(action string, err error) error {
	if errors.Is(err, skillclient.ErrServerUnreachable) {
		return fmt.Errorf("failed to %s: %w\nHint: ensure 'thv serve' is running", action, err)
	}
	return fmt.Errorf("failed to %s: %w", action, err)
}

// validateSkillScope returns a PreRunE that validates the --scope flag.
func validateSkillScope(scopeVar *string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return skills.ValidateScope(skills.Scope(*scopeVar))
	}
}

// validateProjectRootForScope returns a PreRunE that ensures --project-root is
// provided when --scope is "project".
func validateProjectRootForScope(scopeVar, projectRootVar *string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		if skills.Scope(*scopeVar) == skills.ScopeProject && *projectRootVar == "" {
			return fmt.Errorf("--project-root is required when --scope is %q", skills.ScopeProject)
		}
		return nil
	}
}
