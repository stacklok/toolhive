package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// defaultRouter is a simple router implementation that uses a RoutingTable
// to map capability names to backend targets.
//
// It is safe for concurrent use through RWMutex locking.
// The RWMutex provides flexibility for both wholesale table replacement
// and future fine-grained updates (e.g., adding/removing individual backends).
type defaultRouter struct {
	mu           sync.RWMutex
	routingTable *vmcp.RoutingTable
}

// NewDefaultRouter creates a new default router instance.
// The router initially has no routing table and will return errors
// until UpdateRoutingTable is called.
func NewDefaultRouter() Router {
	return &defaultRouter{}
}

// RouteTool resolves a tool name to its backend target.
func (r *defaultRouter) RouteTool(_ context.Context, toolName string) (*vmcp.BackendTarget, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.routingTable == nil {
		return nil, fmt.Errorf("routing table not initialized")
	}

	if r.routingTable.Tools == nil {
		return nil, fmt.Errorf("routing table tools map not initialized")
	}

	target, exists := r.routingTable.Tools[toolName]
	if !exists {
		logger.Debugf("Tool not found in routing table: %s", toolName)
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, toolName)
	}

	logger.Debugf("Routed tool %s to backend %s", toolName, target.WorkloadID)
	return target, nil
}

// RouteResource resolves a resource URI to its backend target.
func (r *defaultRouter) RouteResource(_ context.Context, uri string) (*vmcp.BackendTarget, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.routingTable == nil {
		return nil, fmt.Errorf("routing table not initialized")
	}

	if r.routingTable.Resources == nil {
		return nil, fmt.Errorf("routing table resources map not initialized")
	}

	target, exists := r.routingTable.Resources[uri]
	if !exists {
		logger.Debugf("Resource not found in routing table: %s", uri)
		return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, uri)
	}

	logger.Debugf("Routed resource %s to backend %s", uri, target.WorkloadID)
	return target, nil
}

// RoutePrompt resolves a prompt name to its backend target.
func (r *defaultRouter) RoutePrompt(_ context.Context, name string) (*vmcp.BackendTarget, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.routingTable == nil {
		return nil, fmt.Errorf("routing table not initialized")
	}

	if r.routingTable.Prompts == nil {
		return nil, fmt.Errorf("routing table prompts map not initialized")
	}

	target, exists := r.routingTable.Prompts[name]
	if !exists {
		logger.Debugf("Prompt not found in routing table: %s", name)
		return nil, fmt.Errorf("%w: %s", ErrPromptNotFound, name)
	}

	logger.Debugf("Routed prompt %s to backend %s", name, target.WorkloadID)
	return target, nil
}

// UpdateRoutingTable updates the router's internal routing table.
// This is called after capability aggregation completes with the
// merged routing information.
//
// The update is atomic - all lookups see either the old table or the new table.
func (r *defaultRouter) UpdateRoutingTable(_ context.Context, table *vmcp.RoutingTable) error {
	if table == nil {
		return fmt.Errorf("routing table cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.routingTable = table

	logger.Infof("Updated routing table: %d tools, %d resources, %d prompts",
		len(table.Tools), len(table.Resources), len(table.Prompts))

	return nil
}
