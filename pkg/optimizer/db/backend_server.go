package db

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// BackendServerOps provides database operations for backend servers
type BackendServerOps struct {
	db *DB
}

// NewBackendServerOps creates a new BackendServerOps instance
func NewBackendServerOps(db *DB) *BackendServerOps {
	return &BackendServerOps{db: db}
}

// Create creates a new backend server
func (ops *BackendServerOps) Create(ctx context.Context, server *models.BackendServer) error {
	// Generate ID if not provided
	if server.ID == "" {
		server.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	server.CreatedAt = now
	server.LastUpdated = now

	// Start transaction
	tx, err := ops.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Insert into mcpservers_backend table
	query := `
		INSERT INTO mcpservers_backend (
			id, name, url, backend_identifier, remote, transport, status,
			registry_server_id, registry_server_name, description, server_embedding,
			"group", last_updated, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = tx.ExecContext(ctx, query,
		server.ID,
		server.Name,
		server.URL,
		server.BackendIdentifier,
		server.Remote,
		server.Transport.String(),
		server.Status.String(),
		server.RegistryServerID,
		server.RegistryServerName,
		server.Description,
		embeddingToBytes(server.ServerEmbedding),
		server.Group,
		server.LastUpdated,
		server.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert backend server: %w", err)
	}

	// Insert embedding into vector table if present
	if len(server.ServerEmbedding) > 0 {
		vecQuery := `INSERT INTO backend_server_vector (server_id, embedding) VALUES (?, ?)`
		_, err = tx.ExecContext(ctx, vecQuery, server.ID, embeddingToBytes(server.ServerEmbedding))
		if err != nil {
			return fmt.Errorf("failed to insert server embedding: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetByID retrieves a backend server by ID
func (ops *BackendServerOps) GetByID(ctx context.Context, id string) (*models.BackendServer, error) {
	query := `
		SELECT id, name, url, backend_identifier, remote, transport, status,
		       registry_server_id, registry_server_name, description, server_embedding,
		       "group", last_updated, created_at
		FROM mcpservers_backend
		WHERE id = ?
	`

	var server models.BackendServer
	var embeddingBytes []byte
	var description sql.NullString
	var registryServerID, registryServerName sql.NullString

	err := ops.db.QueryRowContext(ctx, query, id).Scan(
		&server.ID,
		&server.Name,
		&server.URL,
		&server.BackendIdentifier,
		&server.Remote,
		&server.Transport,
		&server.Status,
		&registryServerID,
		&registryServerName,
		&description,
		&embeddingBytes,
		&server.Group,
		&server.LastUpdated,
		&server.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("backend server not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query backend server: %w", err)
	}

	// Set nullable fields
	if description.Valid {
		server.Description = &description.String
	}
	if registryServerID.Valid {
		server.RegistryServerID = &registryServerID.String
	}
	if registryServerName.Valid {
		server.RegistryServerName = &registryServerName.String
	}

	// Convert embedding bytes to float32 slice
	if len(embeddingBytes) > 0 {
		server.ServerEmbedding = bytesToEmbedding(embeddingBytes)
	}

	return &server, nil
}

// GetByName retrieves a backend server by name
func (ops *BackendServerOps) GetByName(ctx context.Context, name string) (*models.BackendServer, error) {
	query := `
		SELECT id, name, url, backend_identifier, remote, transport, status,
		       registry_server_id, registry_server_name, description, server_embedding,
		       "group", last_updated, created_at
		FROM mcpservers_backend
		WHERE name = ?
	`

	var server models.BackendServer
	var embeddingBytes []byte
	var description sql.NullString
	var registryServerID, registryServerName sql.NullString

	err := ops.db.QueryRowContext(ctx, query, name).Scan(
		&server.ID,
		&server.Name,
		&server.URL,
		&server.BackendIdentifier,
		&server.Remote,
		&server.Transport,
		&server.Status,
		&registryServerID,
		&registryServerName,
		&description,
		&embeddingBytes,
		&server.Group,
		&server.LastUpdated,
		&server.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // Not found, return nil without error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query backend server: %w", err)
	}

	// Set nullable fields
	if description.Valid {
		server.Description = &description.String
	}
	if registryServerID.Valid {
		server.RegistryServerID = &registryServerID.String
	}
	if registryServerName.Valid {
		server.RegistryServerName = &registryServerName.String
	}

	// Convert embedding bytes to float32 slice
	if len(embeddingBytes) > 0 {
		server.ServerEmbedding = bytesToEmbedding(embeddingBytes)
	}

	return &server, nil
}

// ListAll retrieves all backend servers
func (ops *BackendServerOps) ListAll(ctx context.Context) ([]*models.BackendServer, error) {
	query := `
		SELECT id, name, url, backend_identifier, remote, transport, status,
		       registry_server_id, registry_server_name, description, server_embedding,
		       "group", last_updated, created_at
		FROM mcpservers_backend
		ORDER BY name
	`

	rows, err := ops.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query backend servers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var servers []*models.BackendServer
	for rows.Next() {
		var server models.BackendServer
		var embeddingBytes []byte
		var description sql.NullString
		var registryServerID, registryServerName sql.NullString

		err := rows.Scan(
			&server.ID,
			&server.Name,
			&server.URL,
			&server.BackendIdentifier,
			&server.Remote,
			&server.Transport,
			&server.Status,
			&registryServerID,
			&registryServerName,
			&description,
			&embeddingBytes,
			&server.Group,
			&server.LastUpdated,
			&server.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan backend server: %w", err)
		}

		// Set nullable fields
		if description.Valid {
			server.Description = &description.String
		}
		if registryServerID.Valid {
			server.RegistryServerID = &registryServerID.String
		}
		if registryServerName.Valid {
			server.RegistryServerName = &registryServerName.String
		}

		// Convert embedding bytes to float32 slice
		if len(embeddingBytes) > 0 {
			server.ServerEmbedding = bytesToEmbedding(embeddingBytes)
		}

		servers = append(servers, &server)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating backend servers: %w", err)
	}

	return servers, nil
}

// Update updates a backend server
func (ops *BackendServerOps) Update(ctx context.Context, server *models.BackendServer) error {
	server.LastUpdated = time.Now()

	tx, err := ops.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		UPDATE mcpservers_backend
		SET name = ?, url = ?, backend_identifier = ?, remote = ?, transport = ?,
		    status = ?, registry_server_id = ?, registry_server_name = ?,
		    description = ?, server_embedding = ?, "group" = ?, last_updated = ?
		WHERE id = ?
	`

	result, err := tx.ExecContext(ctx, query,
		server.Name,
		server.URL,
		server.BackendIdentifier,
		server.Remote,
		server.Transport.String(),
		server.Status.String(),
		server.RegistryServerID,
		server.RegistryServerName,
		server.Description,
		embeddingToBytes(server.ServerEmbedding),
		server.Group,
		server.LastUpdated,
		server.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update backend server: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("backend server not found: %s", server.ID)
	}

	// Update embedding in vector table
	if len(server.ServerEmbedding) > 0 {
		// Delete old embedding
		_, err = tx.ExecContext(ctx, "DELETE FROM backend_server_vector WHERE server_id = ?", server.ID)
		if err != nil {
			return fmt.Errorf("failed to delete old server embedding: %w", err)
		}

		// Insert new embedding
		vecQuery := `INSERT INTO backend_server_vector (server_id, embedding) VALUES (?, ?)`
		_, err = tx.ExecContext(ctx, vecQuery, server.ID, embeddingToBytes(server.ServerEmbedding))
		if err != nil {
			return fmt.Errorf("failed to insert server embedding: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// Delete deletes a backend server and its tools
func (ops *BackendServerOps) Delete(ctx context.Context, id string) error {
	tx, err := ops.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete from vector table
	_, err = tx.ExecContext(ctx, "DELETE FROM backend_server_vector WHERE server_id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete server embedding: %w", err)
	}

	// Delete from main table (CASCADE will delete tools)
	result, err := tx.ExecContext(ctx, "DELETE FROM mcpservers_backend WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete backend server: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("backend server not found: %s", id)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// Helper functions for embedding conversion

// embeddingToBytes converts a float32 slice to bytes for storage
func embeddingToBytes(embedding []float32) []byte {
	if len(embedding) == 0 {
		return nil
	}

	bytes := make([]byte, len(embedding)*4)
	for i, f := range embedding {
		binary.LittleEndian.PutUint32(bytes[i*4:], math.Float32bits(f))
	}
	return bytes
}

// bytesToEmbedding converts bytes to a float32 slice
func bytesToEmbedding(bytes []byte) []float32 {
	if len(bytes) == 0 {
		return nil
	}

	embedding := make([]float32, len(bytes)/4)
	for i := range embedding {
		bits := binary.LittleEndian.Uint32(bytes[i*4:])
		embedding[i] = math.Float32frombits(bits)
	}
	return embedding
}
