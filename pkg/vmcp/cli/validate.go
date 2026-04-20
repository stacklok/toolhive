// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ValidateConfig holds parameters for the validate command.
type ValidateConfig struct {
	// ConfigPath is the path to the vMCP YAML configuration file to validate.
	ConfigPath string
}

// Validate loads and validates a vMCP configuration file, printing a summary
// on success. Returns a descriptive error if the file is missing, malformed,
// or fails semantic validation.
func Validate(_ context.Context, cfg ValidateConfig) error {
	if cfg.ConfigPath == "" {
		return fmt.Errorf("no configuration file specified, use --config flag")
	}

	slog.Info(fmt.Sprintf("Validating configuration: %s", cfg.ConfigPath))

	envReader := &env.OSReader{}
	loader := config.NewYAMLLoader(cfg.ConfigPath, envReader)
	vmcpCfg, err := loader.Load()
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to load configuration: %v", err))
		return fmt.Errorf("configuration loading failed: %w", err)
	}

	slog.Debug("configuration loaded successfully, performing validation")

	validator := config.NewValidator()
	if err := validator.Validate(vmcpCfg); err != nil {
		slog.Error(fmt.Sprintf("Configuration validation failed: %v", err))
		return fmt.Errorf("validation failed: %w", err)
	}

	slog.Info("✓ Configuration is valid")
	slog.Info(fmt.Sprintf("  Name: %s", vmcpCfg.Name))
	slog.Info(fmt.Sprintf("  Group: %s", vmcpCfg.Group))
	slog.Info(fmt.Sprintf("  Incoming Auth: %s", vmcpCfg.IncomingAuth.Type))
	slog.Info(fmt.Sprintf("  Outgoing Auth: %s (source: %s)",
		func() string {
			if len(vmcpCfg.OutgoingAuth.Backends) > 0 {
				return fmt.Sprintf("%d backends configured", len(vmcpCfg.OutgoingAuth.Backends))
			}
			return "default only"
		}(),
		vmcpCfg.OutgoingAuth.Source))
	slog.Info(fmt.Sprintf("  Conflict Resolution: %s", vmcpCfg.Aggregation.ConflictResolution))

	if len(vmcpCfg.CompositeTools) > 0 {
		slog.Info(fmt.Sprintf("  Composite Tools: %d defined", len(vmcpCfg.CompositeTools)))
	}

	return nil
}
