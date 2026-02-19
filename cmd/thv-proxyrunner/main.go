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
