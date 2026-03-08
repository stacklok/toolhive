// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the ToolHive ProxyRunner.
package main

import (
	"log/slog"
	"os"

	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/logging"
	"github.com/stacklok/toolhive/cmd/thv-proxyrunner/app"
)

func main() {
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

	// Skip update check for completion command or if we are running in kubernetes
	if err := app.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
