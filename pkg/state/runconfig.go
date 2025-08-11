package state

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/errors"
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
func DeleteSavedRunConfig(ctx context.Context, name string, logger *zap.SugaredLogger) error {
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

// RunConfigPersister defines an interface for objects that can be persisted and loaded as JSON
type RunConfigPersister interface {
	// WriteJSON serializes the object to JSON and writes it to the provided writer
	WriteJSON(w io.Writer) error
	// GetBaseName returns the base name used for persistence
	GetBaseName() string
}

// ReadJSONFunc defines a function type for reading JSON into an object
type ReadJSONFunc[T any] func(r io.Reader) (T, error)

// SaveRunConfig saves a run configuration to the state store
func SaveRunConfig[T RunConfigPersister](ctx context.Context, config T, logger *zap.SugaredLogger) error {
	// Create a state store
	store, err := NewRunConfigStore(DefaultAppName)
	if err != nil {
		return fmt.Errorf("failed to create state store: %w", err)
	}

	// Get a writer for the state
	writer, err := store.GetWriter(ctx, config.GetBaseName())
	if err != nil {
		return fmt.Errorf("failed to get writer for state: %w", err)
	}
	defer writer.Close()

	// Serialize the configuration to JSON and write it directly to the state store
	if err := config.WriteJSON(writer); err != nil {
		return fmt.Errorf("failed to write run configuration: %w", err)
	}

	logger.Infof("Saved run configuration for %s", config.GetBaseName())
	return nil
}

// LoadRunConfig loads a run configuration from the state store using the provided reader function
func LoadRunConfig[T any](ctx context.Context, name string, readJSONFunc ReadJSONFunc[T]) (T, error) {
	var zero T
	reader, err := LoadRunConfigJSON(ctx, name)
	if err != nil {
		return zero, err
	}
	defer reader.Close()

	// Deserialize the configuration using the provided function
	return readJSONFunc(reader)
}

// ReadRunConfigJSON deserializes a run configuration from JSON read from the provided reader
// This is a generic JSON deserializer for any type that can be unmarshalled from JSON
func ReadRunConfigJSON[T any](r io.Reader) (*T, error) {
	var config T
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// LoadRunConfigOfType loads a run configuration of a specific type T from the state store
func LoadRunConfigOfType[T any](ctx context.Context, name string) (*T, error) {
	return LoadRunConfig(ctx, name, ReadRunConfigJSON[T])
}

// RunConfigReadJSONFunc defines the function signature for reading a RunConfig from JSON
// This allows us to accept the runner.ReadJSON function without creating a circular dependency
type RunConfigReadJSONFunc func(r io.Reader) (interface{}, error)

// LoadRunConfigWithFunc loads a run configuration using a provided read function
func LoadRunConfigWithFunc(ctx context.Context, name string, readFunc RunConfigReadJSONFunc) (interface{}, error) {
	reader, err := LoadRunConfigJSON(ctx, name)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return readFunc(reader)
}

// ReadJSON deserializes JSON from the provided reader into a generic interface
// This function is moved from the runner package to avoid circular dependencies
func ReadJSON(r io.Reader, target interface{}) error {
	decoder := json.NewDecoder(r)
	return decoder.Decode(target)
}
