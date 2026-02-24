// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the ToolHive CLI.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/cmd/thv/app"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/migration"
)

func main() {
	// Initialize the logger
	var opts []logging.Option
	if viper.GetBool("debug") {
		opts = append(opts, logging.WithLevel(slog.LevelDebug))
	}
	l := logging.New(opts...)
	slog.SetDefault(l)

	// Setup signal handling for graceful cleanup
	ctx := setupSignalHandler()

	// Clean up stale lock files on startup
	cleanupStaleLockFiles()

	// Check if container runtime is available early, but skip for informational commands
	if !app.IsInformationalCommand(os.Args) {
		if err := container.CheckRuntimeAvailable(); err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
	}

	// Skip migrations for informational commands that don't need container runtime
	if !app.IsInformationalCommand(os.Args) {
		// Check and perform telemetry config migration if needed
		// Converts telemetry_config.samplingRate from float64 to string in run configs
		migration.CheckAndPerformTelemetryConfigMigration()

		// Check and perform middleware telemetry migration if needed
		// Ensures middleware-based telemetry configs are properly migrated
		migration.CheckAndPerformMiddlewareTelemetryMigration()

		// Ensure default group exists (creates it for fresh installs, no-op otherwise)
		migration.EnsureDefaultGroupExists()
	}

	cmd := app.NewRootCmd(!app.IsCompletionCommand(os.Args))

	// Skip update check for completion command or if we are running in kubernetes
	if err := cmd.ExecuteContext(ctx); err != nil {
		// Clean up any remaining lock files on error exit
		lockfile.CleanupAllLocks()
		os.Exit(1)
	}

	// Clean up lock files on normal exit
	lockfile.CleanupAllLocks()
}

// setupSignalHandler configures signal handling to ensure lock files are cleaned up
func setupSignalHandler() context.Context {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigCh
		slog.Debug("received signal, cleaning up lock files")
		lockfile.CleanupAllLocks()
		cancel()
	}()

	return ctx
}

// cleanupStaleLockFiles removes stale lock files from known directories on startup
func cleanupStaleLockFiles() {
	// Common directories where lock files are created
	var directories []string

	// Config directory
	if configDir, err := xdg.ConfigFile("toolhive"); err == nil {
		directories = append(directories, configDir)
	}

	// Data directory (for statuses and updates)
	if dataDir, err := xdg.DataFile("toolhive"); err == nil {
		directories = append(directories, dataDir)

		// Specific subdirectories
		if statusDir, err := xdg.DataFile("toolhive/statuses"); err == nil {
			directories = append(directories, statusDir)
		}
	}

	// Clean up lock files older than 5 minutes (should be safe for most operations)
	lockfile.CleanupStaleLocks(directories, 5*time.Minute)
}
