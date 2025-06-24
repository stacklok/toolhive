package app

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/runner"
)

// configureRunConfig configures a RunConfig with transport, ports, permissions, etc.
func configureRunConfig(
	config *runner.RunConfig,
	transport string,
	port int,
	targetPort int,
	envVarStrings []string,
) error {
	var err error

	// Set transport
	if _, err = config.WithTransport(transport); err != nil {
		return err
	}

	// Configure ports and target host
	if _, err = config.WithPorts(port, targetPort); err != nil {
		return err
	}

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

	// Add audit configuration if provided
	if config.AuditConfigPath != "" {
		auditConfig, err := audit.LoadFromFile(config.AuditConfigPath)
		if err != nil {
			return fmt.Errorf("failed to load audit configuration: %v", err)
		}
		config.WithAudit(auditConfig)
	}
	// Note: AuditConfig is already set from --enable-audit flag if provided

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
