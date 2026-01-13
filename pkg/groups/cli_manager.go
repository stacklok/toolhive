// Package groups provides functionality for managing logical groupings of MCP servers.
// This file contains the CLI/filesystem-based implementation for local environments.
package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/validation"
)

// cliManager implements the Manager interface using filesystem-based state storage
type cliManager struct {
	groupStore state.Store
}

// NewCLIManager creates a new CLI-based group manager that uses filesystem storage
func NewCLIManager() (Manager, error) {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create group state store: %w", err)
	}

	return &cliManager{groupStore: store}, nil
}

// Create creates a new group with the given name
func (m *cliManager) Create(ctx context.Context, name string) error {
	// Validate group name
	if err := validation.ValidateGroupName(name); err != nil {
		return fmt.Errorf("%w: %s - %w", ErrInvalidGroupName, name, err)
	}
	// Check if group already exists
	exists, err := m.groupStore.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return fmt.Errorf("%w: %s", ErrGroupAlreadyExists, name)
	}

	group := &Group{
		Name:              name,
		RegisteredClients: []string{},
	}
	return m.saveGroup(ctx, group)
}

// Get retrieves a group by name
func (m *cliManager) Get(ctx context.Context, name string) (*Group, error) {
	reader, err := m.groupStore.GetReader(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get reader for group: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			// Non-fatal: reader cleanup failure
			logger.Debugf("Failed to close reader: %v", err)
		}
	}()

	var group Group
	if err := json.NewDecoder(reader).Decode(&group); err != nil {
		return nil, fmt.Errorf("failed to decode group: %w", err)
	}

	return &group, nil
}

// List returns all groups
func (m *cliManager) List(ctx context.Context) ([]*Group, error) {
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

	// Sort groups alphanumerically by name (handles mixed characters, numbers, etc.)
	sort.Slice(groups, func(i, j int) bool {
		return strings.Compare(groups[i].Name, groups[j].Name) < 0
	})

	return groups, nil
}

// Delete removes a group by name
func (m *cliManager) Delete(ctx context.Context, name string) error {
	return m.groupStore.Delete(ctx, name)
}

// Exists checks if a group exists
func (m *cliManager) Exists(ctx context.Context, name string) (bool, error) {
	return m.groupStore.Exists(ctx, name)
}

// RegisterClients registers multiple clients with multiple groups
func (m *cliManager) RegisterClients(ctx context.Context, groupNames []string, clientNames []string) error {
	for _, groupName := range groupNames {
		// Get the existing group
		group, err := m.Get(ctx, groupName)
		if err != nil {
			return fmt.Errorf("failed to get group %s: %w", groupName, err)
		}

		groupModified := false
		for _, clientName := range clientNames {
			// Check if client is already registered
			alreadyRegistered := false
			for _, existingClient := range group.RegisteredClients {
				if existingClient == clientName {
					alreadyRegistered = true
					break
				}
			}

			if alreadyRegistered {
				logger.Infof("Client %s is already registered with group %s, skipping", clientName, groupName)
				continue
			}

			// Add the client to the group
			group.RegisteredClients = append(group.RegisteredClients, clientName)
			groupModified = true
			logger.Infof("Successfully registered client %s with group %s", clientName, groupName)
		}

		// Only save if the group was actually modified
		if groupModified {
			err = m.saveGroup(ctx, group)
			if err != nil {
				return fmt.Errorf("failed to save group %s: %w", groupName, err)
			}
		}
	}

	return nil
}

// UnregisterClients removes multiple clients from multiple groups
func (m *cliManager) UnregisterClients(ctx context.Context, groupNames []string, clientNames []string) error {
	for _, groupName := range groupNames {
		// Get the existing group
		group, err := m.Get(ctx, groupName)
		if err != nil {
			return fmt.Errorf("failed to get group %s: %w", groupName, err)
		}

		groupModified := false
		for _, clientName := range clientNames {
			// Find and remove the client from the group
			for i, existingClient := range group.RegisteredClients {
				if existingClient == clientName {
					// Remove client from slice
					group.RegisteredClients = append(group.RegisteredClients[:i], group.RegisteredClients[i+1:]...)
					groupModified = true
					logger.Infof("Successfully unregistered client %s from group %s", clientName, groupName)
					break
				}
			}
		}

		// Only save if the group was actually modified
		if groupModified {
			err = m.saveGroup(ctx, group)
			if err != nil {
				return fmt.Errorf("failed to save group %s: %w", groupName, err)
			}
		}
	}

	return nil
}

// saveGroup saves the group to the group state store
func (m *cliManager) saveGroup(ctx context.Context, group *Group) error {
	writer, err := m.groupStore.GetWriter(ctx, group.Name)
	if err != nil {
		return fmt.Errorf("failed to get writer for group: %w", err)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			// Non-fatal: writer cleanup failure
			logger.Warnf("Failed to close writer: %v", err)
		}
	}()

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
