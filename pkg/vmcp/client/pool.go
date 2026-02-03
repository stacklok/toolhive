// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/mark3labs/mcp-go/client"
)

// BackendClientPool manages a pool of initialized MCP clients for a single session.
// Each client is keyed by backend ID and reused across multiple tool calls.
//
// Thread-safe for concurrent access.
type BackendClientPool struct {
	clients map[string]*client.Client
	mu      sync.RWMutex
}

// NewBackendClientPool creates a new empty client pool.
func NewBackendClientPool() *BackendClientPool {
	return &BackendClientPool{
		clients: make(map[string]*client.Client),
	}
}

// GetOrCreate returns a cached client for the given backend ID, or creates one using the factory.
// The factory is only called if no healthy client exists for the backend.
//
// Thread-safe: Multiple goroutines can call this concurrently.
func (p *BackendClientPool) GetOrCreate(
	ctx context.Context,
	backendID string,
	factory func(context.Context) (*client.Client, error),
) (*client.Client, error) {
	// Fast path: Check if client already exists (read lock)
	p.mu.RLock()
	if c, ok := p.clients[backendID]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	// Slow path: Create new client (write lock)
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check: Another goroutine might have created it while we waited
	if c, ok := p.clients[backendID]; ok {
		return c, nil
	}

	// Create and initialize new client
	c, err := factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for backend %s: %w", backendID, err)
	}

	// Store in pool
	p.clients[backendID] = c
	return c, nil
}

// MarkUnhealthy removes a client from the pool, forcing recreation on next access.
// Call this when a client experiences connection errors (EOF, reset, etc.).
func (p *BackendClientPool) MarkUnhealthy(backendID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.clients[backendID]; ok {
		// Best-effort close (ignore errors, check for nil)
		if c != nil {
			_ = c.Close()
		}
		delete(p.clients, backendID)
	}
}

// Close shuts down all clients in the pool.
// Called automatically when the session expires.
func (p *BackendClientPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for backendID, c := range p.clients {
		if c != nil {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("failed to close client for backend %s: %w", backendID, err)
			}
		}
	}

	// Clear the map
	p.clients = make(map[string]*client.Client)
	return firstErr
}
