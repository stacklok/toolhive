package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/runner"
)

// RunMCPServer runs an MCP server with the specified configuration.
func RunMCPServer(ctx context.Context, config *runner.RunConfig, foreground bool) error {
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create lifecycle manager: %v", err)
	}

	// If we are running the container in the foreground - call the RunContainer method directly.
	if foreground {
		return manager.RunContainer(ctx, config)
	}

	return manager.RunContainerDetached(config)
}

// configureRunConfig configures a RunConfig with transport, ports, permissions, etc.
func configureRunConfig(
	config *runner.RunConfig,
	transport string,
	port int,
	targetPort int,
	targetHost string,
	envVarStrings []string,
) error {
	var err error

	// Check if permission profile is provided
	if config.PermissionProfileNameOrPath == "" {
		return fmt.Errorf("permission profile is required")
	}

	// Set transport
	if _, err = config.WithTransport(transport); err != nil {
		return err
	}

	// Configure ports and target host
	if _, err = config.WithPorts(port, targetPort); err != nil {
		return err
	}

	// Set target host
	config.TargetHost = targetHost

	// Set permission profile (mandatory)
	if _, err = config.ParsePermissionProfile(); err != nil {
		return err
	}

	// Process volume mounts
	if err = config.ProcessVolumeMounts(); err != nil {
		return err
	}

	// Parse and set environment variables
	if _, err = config.WithEnvironmentVariables(envVarStrings); err != nil {
		return err
	}

	// Generate container name if not already set
	config.WithContainerName()

	// Add standard labels
	config.WithStandardLabels()

	// Add authorization configuration if provided
	if config.AuthzConfigPath != "" {
		authzConfig, err := authz.LoadConfig(config.AuthzConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load authorization configuration: %v", err)
		}
		config.WithAuthz(authzConfig)
	}

	return nil
}

func findEnvironmentVariableFromSecrets(secs []string, envVarName string) bool {
	for _, secret := range secs {
		if isSecretReferenceEnvVar(secret, envVarName) {
			return true
		}
	}

	return false
}

func isSecretReferenceEnvVar(secret, envVarName string) bool {
	parts := strings.Split(secret, ",")
	if len(parts) != 2 {
		return false
	}

	targetSplit := strings.Split(parts[1], "=")
	if len(targetSplit) != 2 {
		return false
	}

	if targetSplit[1] == envVarName {
		return true
	}

	return false
}
