package registry

// Provider defines the interface for registry storage implementations
type Provider interface {
	// GetRegistry returns the complete registry data
	GetRegistry() (*Registry, error)

	// Container server methods
	GetServer(name string) (*ImageMetadata, error)
	SearchServers(query string) ([]*ImageMetadata, error)
	ListServers() ([]*ImageMetadata, error)

	// Remote server methods
	GetRemoteServer(name string) (*RemoteServerMetadata, error)
	SearchRemoteServers(query string) ([]*RemoteServerMetadata, error)
	ListRemoteServers() ([]*RemoteServerMetadata, error)

	// Combined methods
	GetAllServers() ([]*ImageMetadata, []*RemoteServerMetadata, error)
	SearchAllServers(query string) ([]*ImageMetadata, []*RemoteServerMetadata, error)
}
