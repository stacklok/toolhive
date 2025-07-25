package groups

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/state"
)

// manager implements the Manager interface
type manager struct {
	groupStore     state.Store
	runconfigStore state.Store
}

// NewManager creates a new group manager
func NewManager() (Manager, error) {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create group state store: %w", err)
	}
	runConfigStore, err := state.NewRunConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create runconfig store: %w", err)
	}

	return &manager{groupStore: store, runconfigStore: runConfigStore}, nil
}

// Create creates a new group with the given name
func (m *manager) Create(ctx context.Context, name string) error {
	// Validate group name
	if name == "" {
		return fmt.Errorf("group name cannot be empty")
	}

	// Check if group already exists
	exists, err := m.groupStore.Exists(ctx, name)
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
	reader, err := m.groupStore.GetReader(ctx, name)
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
	names, err := m.groupStore.List(ctx)
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
	return m.groupStore.Delete(ctx, name)
}

// Exists checks if a group exists
func (m *manager) Exists(ctx context.Context, name string) (bool, error) {
	return m.groupStore.Exists(ctx, name)
}

// GetWorkloadGroup returns the group that a workload belongs to, if any
func (m *manager) GetWorkloadGroup(ctx context.Context, workloadName string) (*Group, error) {
	runnerInstance, err := runner.LoadState(ctx, workloadName)
	if err != nil {
		if errors.IsRunConfigNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	// If the workload has no group, return nil
	if runnerInstance.Config.Group == "" {
		return nil, nil
	}

	// Get the group details
	return m.Get(ctx, runnerInstance.Config.Group)
}

// ListWorkloadsInGroup returns all workload names that belong to the specified group
func (m *manager) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
	// List all workload names
	workloadNames, err := m.runconfigStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list runconfigs: %w", err)
	}

	// Filter workloads that belong to the specified group
	var groupWorkloads []string

	for _, workloadName := range workloadNames {
		// Load the workload
		runnerInstance, err := runner.LoadState(ctx, workloadName)
		if err != nil {
			logger.Warnf("Failed to load workload %s: %v", workloadName, err)
			continue
		}

		// Check if this workload belongs to the specified group
		if runnerInstance.Config.Group == groupName {
			groupWorkloads = append(groupWorkloads, workloadName)
		}
	}

	return groupWorkloads, nil
}

// saveGroup saves the group to the group state store
func (m *manager) saveGroup(ctx context.Context, group *Group) error {
	writer, err := m.groupStore.GetWriter(ctx, group.Name)
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
