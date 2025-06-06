package registry

// GetRegistry returns the MCP server registry
// Deprecated: Use GetDefaultProvider().GetRegistry() for new code
func GetRegistry() (*Registry, error) {
	provider, err := GetDefaultProvider()
	if err != nil {
		return nil, err
	}
	return provider.GetRegistry()
}

// GetServer returns a server from the registry by name
// Deprecated: Use GetDefaultProvider().GetServer() for new code
func GetServer(name string) (*Server, error) {
	provider, err := GetDefaultProvider()
	if err != nil {
		return nil, err
	}
	return provider.GetServer(name)
}

// SearchServers searches for servers in the registry
// It searches in server names, descriptions, and tags
// Deprecated: Use GetDefaultProvider().SearchServers() for new code
func SearchServers(query string) ([]*Server, error) {
	provider, err := GetDefaultProvider()
	if err != nil {
		return nil, err
	}
	return provider.SearchServers(query)
}

// ListServers returns all servers in the registry
// Deprecated: Use GetDefaultProvider().ListServers() for new code
func ListServers() ([]*Server, error) {
	provider, err := GetDefaultProvider()
	if err != nil {
		return nil, err
	}
	return provider.ListServers()
}
