// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the regup command
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/regup/app"
	"github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger system
	logger.Initialize()

	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}
}
