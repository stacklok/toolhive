// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
	pluginclient "github.com/stacklok/toolhive/pkg/plugins/client"
)

// newAIPluginClient creates a new Plugins API HTTP client using default settings.
// The context is used for server discovery; it is not stored.
func newAIPluginClient(ctx context.Context) *pluginclient.Client {
	return pluginclient.NewDefaultClient(ctx)
}

// completeAIPluginNames provides shell completion for installed plugin names.
func completeAIPluginNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	c := newAIPluginClient(cmd.Context())
	installed, err := c.List(cmd.Context(), plugins.ListOptions{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	names := make([]string, 0, len(installed))
	for _, p := range installed {
		names = append(names, p.Metadata.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// formatAIPluginError wraps an error with contextual information, appending a
// hint that matches the actual failure — a timed-out request and an absent
// server need different advice.
func formatAIPluginError(action string, err error) error {
	switch {
	case errors.Is(err, pluginclient.ErrRequestTimeout):
		return fmt.Errorf(
			"failed to %s: %w\nHint: the server is running and was still working; "+
				"raise the limit with TOOLHIVE_API_TIMEOUT (e.g. TOOLHIVE_API_TIMEOUT=30m)",
			action, err)
	case errors.Is(err, pluginclient.ErrServerUnreachable):
		return fmt.Errorf("failed to %s: %w\nHint: ensure 'thv serve' is running", action, err)
	default:
		return fmt.Errorf("failed to %s: %w", action, err)
	}
}

// validateAIPluginScope returns a PreRunE that validates the --scope flag.
func validateAIPluginScope(scopeVar *string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return plugins.ValidateScope(plugins.Scope(*scopeVar))
	}
}
