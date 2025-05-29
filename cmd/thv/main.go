// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/thv/app"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger
	logger.Initialize()

	// Skip update check for completion command or if we are running in kubernetes
	if err := app.NewRootCmd(!app.IsCompletionCommand(os.Args) && !container.IsKubernetesRuntime()).Execute(); err != nil {
		os.Exit(1)
	}
}
