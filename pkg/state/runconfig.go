package state

import (
	"context"
	"fmt"
	"io"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/logger"
)

// LoadRunConfigJSON loads a run configuration from the state store and returns the raw reader
func LoadRunConfigJSON(ctx context.Context, name string) (io.ReadCloser, error) {
	// Create a state store
	store, err := NewRunConfigStore(DefaultAppName)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	// Check if the configuration exists
	exists, err := store.Exists(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to check if run configuration exists: %w", err)
	}
	if !exists {
		return nil, errors.NewRunConfigNotFoundError(fmt.Sprintf("run configuration for %s not found", name), nil)
	}

	// Get a reader for the state
	reader, err := store.GetReader(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get reader for state: %w", err)
	}

	return reader, nil
}

// DeleteSavedRunConfig deletes a saved run configuration
func DeleteSavedRunConfig(ctx context.Context, name string) error {
	// Create a state store
	store, err := NewRunConfigStore(DefaultAppName)
	if err != nil {
		return fmt.Errorf("failed to create state store: %w", err)
	}

	// Check if the configuration exists
	exists, err := store.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to check if run configuration exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("run configuration for %s not found", name)
	}

	// Delete the configuration
	if err := store.Delete(ctx, name); err != nil {
		return fmt.Errorf("failed to delete run configuration: %w", err)
	}

	logger.Infof("Deleted run configuration for %s", name)
	return nil
}
