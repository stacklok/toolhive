// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/logging"
)

// Run is the proxyrunner's entry point. It binds TOOLHIVE_DEBUG from the
// environment, initializes the slog default logger, constructs a
// signal-aware context honoring SIGINT/SIGTERM/SIGQUIT, and executes the
// root cobra command.
//
// Run is intended to be called from func main() with no other setup so
// that out-of-tree wrappers can be a literal `func main() { app.Run() }`.
// Run never returns; it calls os.Exit(1) on failure.
func Run() {
	// Bind TOOLHIVE_DEBUG env var early, before logger initialization.
	// This must happen before viper.GetBool("debug") so the env var
	// is available when configuring the log level.
	if err := viper.BindEnv("debug", "TOOLHIVE_DEBUG"); err != nil {
		slog.Error("failed to bind TOOLHIVE_DEBUG env var", "error", err)
	}

	// Initialize the logger
	var opts []logging.Option
	if viper.GetBool("debug") {
		opts = append(opts, logging.WithLevel(slog.LevelDebug))
	}
	l := logging.New(opts...)
	slog.SetDefault(l)

	// Create a signal-aware context so SIGTERM from Kubernetes pod lifecycle,
	// SIGQUIT, and os.Interrupt all trigger graceful connection drain via
	// transportHandler.Stop rather than abrupt process exit.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()

	if err := NewRootCmd().ExecuteContext(ctx); err != nil {
		slog.Error("error executing command", "error", err)
		os.Exit(1)
	}
}
