// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the ToolHive Kubernetes Operator.
// It sets up and runs the controller manager for the MCPServer custom resource.
package main

import "github.com/stacklok/toolhive/cmd/thv-operator/app"

func main() { app.Run() }
