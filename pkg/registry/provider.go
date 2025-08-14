package registry

//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=provider.go Provider

// Provider defines the interface for registry storage implementations
type Provider interface {
	// GetRegistry returns the complete registry data
	GetRegistry() (*Registry, error)

	// GetServer returns a specific server by name (container or remote)
	GetServer(name string) (ServerMetadata, error)

	// SearchServers searches for servers matching the query (both container and remote)
	SearchServers(query string) ([]ServerMetadata, error)

	// ListServers returns all available servers (both container and remote)
	ListServers() ([]ServerMetadata, error)

	// Legacy methods for backward compatibility
	// GetImageServer returns a specific container server by name
	GetImageServer(name string) (*ImageMetadata, error)

	// SearchImageServers searches for container servers matching the query
	SearchImageServers(query string) ([]*ImageMetadata, error)

	// ListImageServers returns all available container servers
	ListImageServers() ([]*ImageMetadata, error)
}
