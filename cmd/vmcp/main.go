// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the Virtual MCP Server (vmcp).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/cmd/vmcp/app"
)

func main() {
	// Install a default INFO-level logger so any early errors (before cobra
	// finishes parsing flags) still produce structured output. The real
	// logger — which honors the --debug flag — is installed in the root
	// command's PersistentPreRunE once viper has seen the parsed flags.
	slog.SetDefault(logging.New())

	// Create a context that will be canceled on signal
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	// Execute the root command with context
	if err := app.NewRootCmd().ExecuteContext(ctx); err != nil {
		slog.Error(fmt.Sprintf("Error executing command: %v", err))
		os.Exit(1)
	}
}
