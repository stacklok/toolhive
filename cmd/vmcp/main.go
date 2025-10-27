// Package main is the entry point for the Virtual MCP Server (vmcp).
package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/stacklok/toolhive/cmd/vmcp/app"
	"github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger
	logger.Initialize()

	// Setup signal handling for graceful cleanup
	setupSignalHandler()

	// Execute the root command
	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Errorf("Error executing command: %v", err)
		os.Exit(1)
	}
}

// setupSignalHandler configures signal handling to ensure graceful shutdown
func setupSignalHandler() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		<-sigCh
		logger.Debugf("Received signal, shutting down gracefully...")
		os.Exit(0)
	}()
}
