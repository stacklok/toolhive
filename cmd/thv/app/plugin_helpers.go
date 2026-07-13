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

// newPluginClient creates a new Plugins API HTTP client using default settings.
// The context is used for server discovery; it is not stored.
func newPluginClient(ctx context.Context) *pluginclient.Client {
	return pluginclient.NewDefaultClient(ctx)
}

// completePluginNames provides shell completion for installed plugin names.
func completePluginNames(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	c := newPluginClient(cmd.Context())
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

// formatPluginError wraps an error with contextual information. If the
// underlying cause is ErrServerUnreachable it appends a helpful hint.
func formatPluginError(action string, err error) error {
	if errors.Is(err, pluginclient.ErrServerUnreachable) {
		return fmt.Errorf("failed to %s: %w\nHint: ensure 'thv serve' is running", action, err)
	}
	return fmt.Errorf("failed to %s: %w", action, err)
}

// validatePluginScope returns a PreRunE that validates the --scope flag.
func validatePluginScope(scopeVar *string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		return plugins.ValidateScope(plugins.Scope(*scopeVar))
	}
}
