// Package groups provides functionality for managing logical groupings of MCP servers.
// It includes types and interfaces for creating, retrieving, listing, and deleting groups.
package groups

import (
	"context"
	"encoding/json"
	"os"
)

// DefaultGroup is the name of the default group for workloads
const DefaultGroup = "default"

// Group represents a logical grouping of MCP servers.
type Group struct {
	Name string `json:"name"`
}

// WriteJSON serializes the Group to JSON and writes it to the provided writer
func (g *Group) WriteJSON(w *os.File) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(g)
}

// Manager defines the interface for managing groups of MCP servers.
// It provides methods for creating, retrieving, listing, and deleting groups.
type Manager interface {
	// Create creates a new group with the specified name.
	// Returns an error if a group with the same name already exists.
	Create(ctx context.Context, name string) error

	// Get retrieves a group by name.
	// Returns an error if the group does not exist.
	Get(ctx context.Context, name string) (*Group, error)

	// List returns all groups.
	List(ctx context.Context) ([]*Group, error)

	// Delete removes a group by name.
	// Returns an error if the group does not exist.
	Delete(ctx context.Context, name string) error

	// Exists checks if a group with the specified name exists.
	Exists(ctx context.Context, name string) (bool, error)

	// GetWorkloadGroup returns the group that a workload belongs to, if any.
	// Returns nil if the workload is not in any group.
	GetWorkloadGroup(ctx context.Context, workloadName string) (*Group, error)

	// ListWorkloadsInGroup returns all workload names that belong to the specified group.
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)
}
