package authserver

import "time"

// StorageType defines the type of storage backend.
type StorageType string

const (
	// StorageTypeMemory uses in-memory storage (default).
	StorageTypeMemory StorageType = "memory"
	// TODO: Add StorageTypeRedis and StorageTypePostgres when persistent storage is needed.
)

// StorageConfig configures the storage backend.
type StorageConfig struct {
	// Type specifies the storage backend type. Defaults to memory.
	Type StorageType

	// CleanupInterval for expired entries (memory storage only).
	CleanupInterval time.Duration
}

// DefaultStorageConfig returns sensible defaults.
func DefaultStorageConfig() *StorageConfig {
	return &StorageConfig{
		Type:            StorageTypeMemory,
		CleanupInterval: DefaultCleanupInterval,
	}
}
