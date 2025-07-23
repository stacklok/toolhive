package groups

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// manager implements the Manager interface
type manager struct {
	store           state.Store
	workloadManager workloads.Manager
}

// NewManager creates a new group manager
func NewManager() (Manager, error) {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create group state store: %w", err)
	}

	workloadManager, err := workloads.NewManager(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	return &manager{store: store, workloadManager: workloadManager}, nil
}

// NewManagerWithWorkloadManager creates a new group manager with a custom workload manager
// This is useful for testing with mocks
func NewManagerWithWorkloadManager(workloadManager workloads.Manager) (Manager, error) {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create group state store: %w", err)
	}

	return &manager{store: store, workloadManager: workloadManager}, nil
}

// Create creates a new group with the given name
func (m *manager) Create(ctx context.Context, name string) error {
	// Validate group name
	if name == "" {
		return fmt.Errorf("group name cannot be empty")
	}

	// Check if group already exists
	exists, err := m.store.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return fmt.Errorf("group '%s' already exists", name)
	}

	group := &Group{Name: name}
	return m.saveGroup(ctx, group)
}

// Get retrieves a group by name
func (m *manager) Get(ctx context.Context, name string) (*Group, error) {
	reader, err := m.store.GetReader(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get reader for group: %w", err)
	}
	defer reader.Close()

	var group Group
	if err := json.NewDecoder(reader).Decode(&group); err != nil {
		return nil, fmt.Errorf("failed to decode group: %w", err)
	}

	return &group, nil
}

// List returns all groups
func (m *manager) List(ctx context.Context) ([]*Group, error) {
	names, err := m.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}

	groups := make([]*Group, 0, len(names))
	for _, name := range names {
		group, err := m.Get(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("failed to get group %s: %w", name, err)
		}
		groups = append(groups, group)
	}

	return groups, nil
}

// Delete removes a group by name
func (m *manager) Delete(ctx context.Context, name string) error {
	return m.store.Delete(ctx, name)
}

// Exists checks if a group exists
func (m *manager) Exists(ctx context.Context, name string) (bool, error) {
	return m.store.Exists(ctx, name)
}

// GetWorkloadGroup returns the group that a workload belongs to, if any
func (m *manager) GetWorkloadGroup(ctx context.Context, workloadName string) (*Group, error) {
	// Use the workload manager to get the workload and check its group label

	// Get the workload details
	workload, err := m.workloadManager.GetWorkload(ctx, workloadName)
	if err != nil {
		// If workload not found, it's not in any group
		return nil, nil
	}

	// Check if the workload has a group
	if workload.Group == "" {
		return nil, nil // Workload is not in any group
	}

	// Get the group details
	return m.Get(ctx, workload.Group)
}

// saveGroup saves the group to the group state store
func (m *manager) saveGroup(ctx context.Context, group *Group) error {
	writer, err := m.store.GetWriter(ctx, group.Name)
	if err != nil {
		return fmt.Errorf("failed to get writer for group: %w", err)
	}
	defer writer.Close()

	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(group); err != nil {
		return fmt.Errorf("failed to write group: %w", err)
	}

	// Ensure the writer is flushed
	if closer, ok := writer.(interface{ Sync() error }); ok {
		if err := closer.Sync(); err != nil {
			return fmt.Errorf("failed to sync group file: %w", err)
		}
	}

	return nil
}
