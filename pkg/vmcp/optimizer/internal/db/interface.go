// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
)

// Database is the main interface for optimizer database operations.
// It provides methods for managing backend servers and tools with hybrid search capabilities.
type Database interface {
	// Server operations
	CreateOrUpdateServer(ctx context.Context, server *models.BackendServer) error
	DeleteServer(ctx context.Context, serverID string) error

	// Tool operations
	CreateTool(ctx context.Context, tool *models.BackendTool, serverName string) error
	DeleteToolsByServer(ctx context.Context, serverID string) error
	SearchToolsHybrid(ctx context.Context, query string, config *HybridSearchConfig) ([]*models.BackendToolWithMetadata, error)
	ListToolsByServer(ctx context.Context, serverID string) ([]*models.BackendTool, error)

	// Statistics
	GetTotalToolTokens(ctx context.Context) (int, error)

	// Lifecycle
	Reset()
	Close() error
}
