package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	thverrors "github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/state"
)

const (
	// DefaultGroupName is the name of the default group
	DefaultGroupName = "default"
)

// manager implements the Manager interface
type manager struct {
	groupStore state.Store
	logger     *zap.SugaredLogger
}

// NewManager creates a new group manager
func NewManager(logger *zap.SugaredLogger) (Manager, error) {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return nil, fmt.Errorf("failed to create group state store: %w", err)
	}

	return &manager{
		groupStore: store,
		logger:     logger,
	}, nil
}

// Create creates a new group with the given name
func (m *manager) Create(ctx context.Context, name string) error {
	// Check if group already exists
	exists, err := m.groupStore.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return thverrors.NewGroupAlreadyExistsError(fmt.Sprintf("group '%s' already exists", name), nil)
	}

	group := &Group{
		Name:              name,
		RegisteredClients: []string{},
	}
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

	// Sort groups alphanumerically by name (handles mixed characters, numbers, etc.)
	sort.Slice(groups, func(i, j int) bool {
		return strings.Compare(groups[i].Name, groups[j].Name) < 0
	})

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

// RegisterClients registers multiple clients with multiple groups
func (m *manager) RegisterClients(ctx context.Context, groupNames []string, clientNames []string) error {
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
				m.logger.Infof("Client %s is already registered with group %s, skipping", clientName, groupName)
				continue
			}

			// Add the client to the group
			group.RegisteredClients = append(group.RegisteredClients, clientName)
			groupModified = true
			m.logger.Infof("Successfully registered client %s with group %s", clientName, groupName)
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
func (m *manager) UnregisterClients(ctx context.Context, groupNames []string, clientNames []string) error {
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
					m.logger.Infof("Successfully unregistered client %s from group %s", clientName, groupName)
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
