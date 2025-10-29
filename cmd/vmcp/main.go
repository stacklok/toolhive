// Package main is the entry point for the Virtual MCP Server (vmcp).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/stacklok/toolhive/cmd/vmcp/app"
	"github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger
	logger.Initialize()

	// Create a context that will be canceled on signal
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	// Execute the root command with context
	if err := app.NewRootCmd().ExecuteContext(ctx); err != nil {
		logger.Errorf("Error executing command: %v", err)
		os.Exit(1)
	}
}
