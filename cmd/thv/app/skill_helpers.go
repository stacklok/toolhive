// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	tclient "github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/skills"
	skillclient "github.com/stacklok/toolhive/pkg/skills/client"
)

// newSkillClient creates a new Skills API HTTP client using default settings.
// The context is used for server discovery; it is not stored.
func newSkillClient(ctx context.Context) *skillclient.Client {
	return skillclient.NewDefaultClient(ctx)
}

// completeSkillNames provides shell completion for installed skill names.
func completeSkillNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	c := newSkillClient(cmd.Context())
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

// formatSkillError wraps an error with contextual information, appending a
// hint that matches the actual failure — a timed-out request and an absent
// server need different advice.
func formatSkillError(action string, err error) error {
	switch {
	case errors.Is(err, skillclient.ErrRequestTimeout):
		return fmt.Errorf(
			"failed to %s: %w\nHint: the server is running and was still working; "+
				"raise the limit with TOOLHIVE_API_TIMEOUT (e.g. TOOLHIVE_API_TIMEOUT=30m)",
			action, err)
	case errors.Is(err, skillclient.ErrServerUnreachable):
		return fmt.Errorf("failed to %s: %w\nHint: ensure 'thv serve' is running", action, err)
	default:
		return fmt.Errorf("failed to %s: %w", action, err)
	}
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

// absProjectRoot makes a user-supplied --project-root absolute, leaving an
// empty value empty — for most commands that means "not project-scoped", and
// substituting the working directory there would silently change the scope.
//
// The API server requires an absolute project root, so a relative value would
// otherwise travel all the way to the server and come back as a validation
// error naming the wire field rather than the flag the user typed.
func absProjectRoot(explicit string) (string, error) {
	if explicit == "" {
		return "", nil
	}
	abs, err := filepath.Abs(explicit)
	if err != nil {
		return "", fmt.Errorf("resolving --project-root %q: %w", explicit, err)
	}
	return abs, nil
}

// resolveProjectRoot returns explicit if set, otherwise auto-detects the
// project root by walking up from the current directory looking for .git —
// used by commands (sync, upgrade) that operate on "the project you're in"
// rather than requiring --project-root on every invocation.
func resolveProjectRoot(explicit string) (string, error) {
	if explicit != "" {
		return absProjectRoot(explicit)
	}
	root, err := tclient.DetectProjectRoot("")
	if err != nil {
		return "", fmt.Errorf("detecting project root: %w (use --project-root to specify it explicitly)", err)
	}
	return root, nil
}
