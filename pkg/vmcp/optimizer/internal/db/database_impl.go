// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
)

// databaseImpl implements the Database interface
type databaseImpl struct {
	db               *chromemDB
	embeddingFunc    chromem.EmbeddingFunc
	backendServerOps *backendServerOps
	backendToolOps   *backendToolOps
}

// NewDatabase creates a new Database instance with the provided configuration and embedding function.
// This is the main entry point for creating a database instance.
func NewDatabase(config *Config, embeddingFunc chromem.EmbeddingFunc) (Database, error) {
	db, err := newChromemDB(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	impl := &databaseImpl{
		db:            db,
		embeddingFunc: embeddingFunc,
	}

	impl.backendServerOps = newBackendServerOps(db, embeddingFunc)
	impl.backendToolOps = newBackendToolOps(db, embeddingFunc)

	return impl, nil
}

// CreateOrUpdateServer creates or updates a backend server
func (d *databaseImpl) CreateOrUpdateServer(ctx context.Context, server *models.BackendServer) error {
	return d.backendServerOps.update(ctx, server)
}

// DeleteServer removes a backend server
func (d *databaseImpl) DeleteServer(ctx context.Context, serverID string) error {
	return d.backendServerOps.delete(ctx, serverID)
}

// CreateTool adds a new backend tool
func (d *databaseImpl) CreateTool(ctx context.Context, tool *models.BackendTool, serverName string) error {
	return d.backendToolOps.create(ctx, tool, serverName)
}

// DeleteToolsByServer removes all tools for a given server
func (d *databaseImpl) DeleteToolsByServer(ctx context.Context, serverID string) error {
	return d.backendToolOps.deleteByServer(ctx, serverID)
}

// SearchToolsHybrid performs hybrid search for backend tools
func (d *databaseImpl) SearchToolsHybrid(
	ctx context.Context,
	query string,
	config *HybridSearchConfig,
) ([]*models.BackendToolWithMetadata, error) {
	return d.backendToolOps.searchHybrid(ctx, query, config)
}

// ListToolsByServer returns all tools for a given server
func (d *databaseImpl) ListToolsByServer(ctx context.Context, serverID string) ([]*models.BackendTool, error) {
	return d.backendToolOps.listByServer(ctx, serverID)
}

// GetTotalToolTokens returns the total token count across all tools
func (d *databaseImpl) GetTotalToolTokens(ctx context.Context) (int, error) {
	// Use FTS database to efficiently count all tool tokens
	if d.db.fts != nil {
		return d.db.fts.GetTotalToolTokens(ctx)
	}
	return 0, fmt.Errorf("FTS database not available")
}

// Reset clears all collections and FTS tables
func (d *databaseImpl) Reset() {
	d.db.reset()
}

// Close releases all database resources
func (d *databaseImpl) Close() error {
	return d.db.close()
}
