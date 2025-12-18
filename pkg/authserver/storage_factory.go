package authserver

import "fmt"

// NewStorage creates a Storage implementation based on config.
// If config is nil, defaults to in-memory storage.
func NewStorage(config *StorageConfig) (Storage, error) {
	if config == nil {
		config = DefaultStorageConfig()
	}

	switch config.Type {
	case StorageTypeMemory, "":
		opts := []MemoryStorageOption{}
		if config.CleanupInterval > 0 {
			opts = append(opts, WithCleanupInterval(config.CleanupInterval))
		}
		return NewMemoryStorage(opts...), nil
	default:
		return nil, fmt.Errorf("unknown storage type: %s", config.Type)
	}
}
