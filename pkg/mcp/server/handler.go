// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package server provides the MCP (Model Context Protocol) server implementation for ToolHive.
package server

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// Handler handles MCP tool requests for ToolHive
type Handler struct {
	ctx             context.Context
	workloadManager workloads.Manager
	registryStore   *registry.Store
	configProvider  config.Provider
}

// NewHandler creates a new ToolHive handler
func NewHandler(ctx context.Context) (*Handler, error) {
	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Create config provider
	configProvider := config.NewProvider()

	store, err := registry.DefaultStore()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry store: %w", err)
	}

	return &Handler{
		ctx:             ctx,
		workloadManager: workloadManager,
		registryStore:   store,
		configProvider:  configProvider,
	}, nil
}
