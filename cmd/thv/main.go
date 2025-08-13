// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/thv/app"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	log "github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	logger := log.NewLogger()
	// Check and perform auto-discovery migration if needed
	// Handles the auto-discovery flag depreciation, only executes once on old config files
	client.CheckAndPerformAutoDiscoveryMigration(logger)

	// Check and perform default group migration if needed
	// Migrates existing workloads to the default group, only executes once
	// TODO: Re-enable when group functionality is complete
	//migration.CheckAndPerformDefaultGroupMigration()

	// Skip update check for completion command or if we are running in kubernetes
	if err := app.NewRootCmd(!app.IsCompletionCommand(os.Args) && !runtime.IsKubernetesRuntime()).Execute(); err != nil {
		os.Exit(1)
	}
}
