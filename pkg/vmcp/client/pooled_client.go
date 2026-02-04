// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/mark3labs/mcp-go/client"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// pooledBackendClient wraps httpBackendClient to add session-scoped client pooling.
// Implements the BackendClient interface by delegating to the underlying client.
//
// This decorator reuses initialized MCP clients across multiple tool calls within
// the same session, preserving backend state (e.g., Playwright browser contexts,
// database transactions, conversation history).
type pooledBackendClient struct {
	*httpBackendClient // Embed to inherit ListCapabilities
	sessionManager     *transportsession.Manager
}

// NewPooledHTTPBackendClient wraps an HTTP backend client with session-scoped pooling.
func NewPooledHTTPBackendClient(
	underlying *httpBackendClient,
	sessionManager *transportsession.Manager,
) vmcp.BackendClient {
	return &pooledBackendClient{
		httpBackendClient: underlying,
		sessionManager:    sessionManager,
	}
}

// WrapWithPooling wraps a BackendClient with session-scoped pooling if it's an HTTP client.
// Returns the wrapped client if successful, or the original client if wrapping is not applicable.
//
// This is the public API for enabling client pooling. Use this instead of type-asserting
// to httpBackendClient directly.
func WrapWithPooling(
	backendClient vmcp.BackendClient,
	sessionManager *transportsession.Manager,
) vmcp.BackendClient {
	// Try to type-assert to httpBackendClient
	if httpClient, ok := backendClient.(*httpBackendClient); ok {
		return NewPooledHTTPBackendClient(httpClient, sessionManager)
	}

	// Not an HTTP client - return unchanged
	return backendClient
}

// getOrCreatePool retrieves or creates a client pool for the given session.
// The pool is stored in VMCPSession and cleaned up automatically by TTL.
func (p *pooledBackendClient) getOrCreatePool(ctx context.Context) (*BackendClientPool, error) {
	// Extract session ID from context (set by discovery middleware)
	sessionID, ok := ctx.Value(transportsession.SessionIDContextKey).(string)
	if !ok || sessionID == "" {
		// Fallback: No session context (shouldn't happen in normal flow)
		// Return a temporary pool that won't be reused
		return NewBackendClientPool(), nil
	}

	// Get VMCPSession from manager
	vmcpSess, err := vmcpsession.GetVMCPSession(sessionID, p.sessionManager)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// Check if pool already exists in session
	if poolInterface := vmcpSess.GetClientPool(); poolInterface != nil {
		// Type assert to concrete type
		if pool, ok := poolInterface.(*BackendClientPool); ok {
			return pool, nil
		}
		return nil, fmt.Errorf("invalid client pool type: %T", poolInterface)
	}

	// Create new pool and store in session
	pool := NewBackendClientPool()
	vmcpSess.SetClientPool(pool)

	return pool, nil
}

// CallTool implements BackendClient.CallTool with client pooling.
func (p *pooledBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	pool, err := p.getOrCreatePool(ctx)
	if err != nil {
		return nil, err
	}

	// Get or create client from pool
	c, err := pool.GetOrCreate(ctx, target.WorkloadID, func(ctx context.Context) (*client.Client, error) {
		// Reuse the underlying client's factory logic
		newClient, err := p.defaultClientFactory(ctx, target)
		if err != nil {
			return nil, err
		}

		// Initialize the client before storing in pool
		if _, err := initializeClient(ctx, newClient); err != nil {
			// Close on initialization failure
			_ = newClient.Close()
			return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
		}

		return newClient, nil
	})
	if err != nil {
		return nil, err
	}

	// Call tool using pooled client (no defer close!)
	result, err := p.callToolWithClient(ctx, c, target, toolName, arguments, meta)
	if err != nil {
		// Check if it's a connection error that warrants marking unhealthy
		if shouldMarkUnhealthy(err) {
			pool.MarkUnhealthy(target.WorkloadID)
		}
		return nil, err
	}

	return result, nil
}

// ReadResource implements BackendClient.ReadResource with client pooling.
func (p *pooledBackendClient) ReadResource(
	ctx context.Context,
	target *vmcp.BackendTarget,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	pool, err := p.getOrCreatePool(ctx)
	if err != nil {
		return nil, err
	}

	c, err := pool.GetOrCreate(ctx, target.WorkloadID, func(ctx context.Context) (*client.Client, error) {
		newClient, err := p.defaultClientFactory(ctx, target)
		if err != nil {
			return nil, err
		}

		if _, err := initializeClient(ctx, newClient); err != nil {
			_ = newClient.Close()
			return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
		}

		return newClient, nil
	})
	if err != nil {
		return nil, err
	}

	result, err := p.readResourceWithClient(ctx, c, target, uri)
	if err != nil {
		if shouldMarkUnhealthy(err) {
			pool.MarkUnhealthy(target.WorkloadID)
		}
		return nil, err
	}

	return result, nil
}

// GetPrompt implements BackendClient.GetPrompt with client pooling.
func (p *pooledBackendClient) GetPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	pool, err := p.getOrCreatePool(ctx)
	if err != nil {
		return nil, err
	}

	c, err := pool.GetOrCreate(ctx, target.WorkloadID, func(ctx context.Context) (*client.Client, error) {
		newClient, err := p.defaultClientFactory(ctx, target)
		if err != nil {
			return nil, err
		}

		if _, err := initializeClient(ctx, newClient); err != nil {
			_ = newClient.Close()
			return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
		}

		return newClient, nil
	})
	if err != nil {
		return nil, err
	}

	result, err := p.getPromptWithClient(ctx, c, target, name, arguments)
	if err != nil {
		if shouldMarkUnhealthy(err) {
			pool.MarkUnhealthy(target.WorkloadID)
		}
		return nil, err
	}

	return result, nil
}

// shouldMarkUnhealthy determines if an error indicates a broken connection
// that warrants removing the client from the pool.
// Returns true for network errors, connection resets, EOFs, and similar issues
// that suggest the client should be recreated.
func shouldMarkUnhealthy(err error) bool {
	if err == nil {
		return false
	}

	// Check for syscall errors first (before net.Error check)
	// syscall.Errno implements net.Error, so we need to check it first
	// to avoid false positives on non-connection syscall errors
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		//nolint:exhaustive // We explicitly check only connection-related errors
		switch syscallErr {
		case syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ECONNABORTED:
			return true
		default:
			// Other syscall errors - don't mark unhealthy
			return false
		}
	}

	// Check for common network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		// Timeout errors might be transient - don't mark unhealthy
		if netErr.Timeout() {
			return false
		}
		// Other network errors (connection refused, reset, etc.) should recreate
		return true
	}

	// Conservative: Don't mark unhealthy for unknown error types
	// This prevents false positives for MCP protocol errors
	return false
}
