package app

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/transport"
)

// runInteractive runs the interactive wizard for configuring an MCP server
func runInteractive(ctx context.Context, cmd *cobra.Command) error {
	// Check if terminal is interactive (not piped)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("interactive mode requires a terminal; stdin is not a TTY")
	}

	// Load registry servers for the wizard
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		logger.Warnf("Failed to load registry: %v", err)
	}

	var servers []regtypes.ServerMetadata
	if provider != nil {
		servers, err = provider.ListServers()
		if err != nil {
			logger.Warnf("Failed to list registry servers: %v", err)
		}
	}

	// Run the wizard
	config, confirmed, err := ui.RunWizard(servers)
	if err != nil {
		return fmt.Errorf("wizard error: %w", err)
	}

	if !confirmed {
		logger.Info("Wizard cancelled")
		return nil
	}

	// Convert wizard config to RunFlags
	flags := wizardConfigToRunFlags(config)

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Determine the server identifier
	serverOrImage := config.ServerOrImage
	if config.IsRemote && config.RemoteURL != "" {
		serverOrImage = config.RemoteURL
	}

	// Run the server with the configured flags
	return runSingleServer(ctx, flags, serverOrImage, config.CmdArgs, debugMode, cmd, "")
}

// wizardConfigToRunFlags converts a WizardConfig to RunFlags
func wizardConfigToRunFlags(config *ui.WizardConfig) *RunFlags {
	flags := &RunFlags{
		Name:      config.Name,
		Transport: config.Transport,
		Group:     config.Group,
		Env:       config.EnvVars,
		Volumes:   config.Volumes,
		// Set default values for host configuration
		Host:       transport.LocalhostIPv4,
		TargetHost: transport.LocalhostIPv4,
		// Set default value for image verification
		VerifyImage: retriever.VerifyImageWarn,
	}

	// Set permission profile if specified
	if config.PermissionProfile != "" {
		flags.PermissionProfile = config.PermissionProfile
	}

	// Handle remote servers
	if config.IsRemote {
		flags.RemoteURL = config.RemoteURL
	}

	// Set default group if not specified
	if flags.Group == "" {
		flags.Group = "default"
	}

	// Ensure ignore globally is enabled by default
	flags.IgnoreGlobally = true

	return flags
}
