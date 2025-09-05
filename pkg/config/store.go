package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
)

// lockTimeout is the maximum time to wait for a file lock
const lockTimeout = 1 * time.Second

// Store defines the interface for configuration storage operations
type Store interface {
	// Load loads the configuration from storage
	Load(ctx context.Context) (*Config, error)
	// Save saves the configuration to storage
	Save(ctx context.Context, config *Config) error
	// Exists checks if configuration exists in storage
	Exists(ctx context.Context) (bool, error)
	// Update performs a locked update operation on the configuration
	Update(ctx context.Context, updateFn func(*Config)) error
}

// LocalStore implements Store using local file system
type LocalStore struct {
	configPath string
}

// NewLocalStore creates a new local file-based configuration store
func NewLocalStore(configPath string) *LocalStore {
	return &LocalStore{
		configPath: configPath,
	}
}

// Load loads configuration from local file
func (s *LocalStore) Load(_ context.Context) (*Config, error) {
	var config Config
	var err error

	configPath := s.configPath
	if configPath == "" {
		configPath, err = getConfigPath()
		if err != nil {
			return nil, fmt.Errorf("unable to fetch config path: %w", err)
		}
	}

	// Check to see if the config file already exists.
	configPath = path.Clean(configPath)
	newConfig := false
	// #nosec G304: File path is not configurable at this time.
	_, err = os.Stat(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			newConfig = true
		} else {
			return nil, fmt.Errorf("failed to stat config file: %w", err)
		}
	}

	if newConfig {
		// Create a new config with default values.
		config = createNewConfigWithDefaults()

		// Persist the new default to disk using the specific path
		logger.Debugf("initializing configuration file at %s", configPath)
		err = config.saveToPath(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to write default config: %w", err)
		}
	} else {
		// Load the existing config and decode.
		// #nosec G304: File path is not configurable at this time.
		configFile, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read config file %s: %w", configPath, err)
		}
		err = yaml.Unmarshal(configFile, &config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse config file yaml: %w", err)
		}

		// Apply backward compatibility fixes
		err = applyBackwardCompatibility(&config)
		if err != nil {
			return nil, fmt.Errorf("failed to apply backward compatibility fixes: %w", err)
		}
	}

	return &config, nil
}

// Save saves configuration to local file
func (s *LocalStore) Save(_ context.Context, config *Config) error {
	if s.configPath == "" {
		return config.save()
	}
	return config.saveToPath(s.configPath)
}

// Exists checks if local config file exists
func (s *LocalStore) Exists(_ context.Context) (bool, error) {
	var configPath string
	var err error

	if s.configPath == "" {
		configPath, err = getConfigPath()
		if err != nil {
			return false, fmt.Errorf("unable to fetch config path: %w", err)
		}
	} else {
		configPath = s.configPath
	}

	_, err = os.Stat(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat config file: %w", err)
	}
	return true, nil
}

// Update performs a locked update operation on the configuration
func (s *LocalStore) Update(ctx context.Context, updateFn func(*Config)) error {
	configPath := s.configPath
	if configPath == "" {
		var err error
		configPath, err = getConfigPath()
		if err != nil {
			return fmt.Errorf("unable to fetch config path: %w", err)
		}
	}

	// Use a separate lock file for cross-platform compatibility
	lockPath := configPath + ".lock"
	fileLock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	// Try and acquire a file lock.
	locked, err := fileLock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire lock: timeout after %v", lockTimeout)
	}
	defer fileLock.Unlock()

	// Load the config after acquiring the lock to avoid race conditions
	config, err := s.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Apply changes to the config
	updateFn(config)

	// Save the updated config
	err = s.Save(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

// NewConfigStore creates a local configuration store
func NewConfigStore() (Store, error) {
	return NewConfigStoreWithDetector("", nil)
}

// NewConfigStoreWithDetector creates a local configuration store (detector parameter ignored)
func NewConfigStoreWithDetector(configPath string, _ interface{}) (Store, error) {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesStore(), nil
	}
	return NewLocalStore(configPath), nil
}
