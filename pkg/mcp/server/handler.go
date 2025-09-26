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
	ctx              context.Context
	workloadManager  workloads.Manager
	registryProvider registry.Provider
	configProvider   config.Provider
}

// NewHandler creates a new ToolHive handler
func NewHandler(ctx context.Context) (*Handler, error) {
	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Create registry provider
	registryProvider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %w", err)
	}

	// Create config provider
	configProvider := config.NewDefaultProvider()

	return &Handler{
		ctx:              ctx,
		workloadManager:  workloadManager,
		registryProvider: registryProvider,
		configProvider:   configProvider,
	}, nil
}
