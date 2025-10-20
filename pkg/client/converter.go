package client

import "fmt"

const (
	// GooseTimeout is the Goose operation timeout in seconds
	GooseTimeout = 60
)

// YAMLConverter defines an interface for converting MCPServer data to different YAML config formats
type YAMLConverter interface {
	ConvertFromMCPServer(serverName string, server MCPServer) (interface{}, error)
	UpsertEntry(config interface{}, serverName string, entry interface{}) error
	RemoveEntry(config interface{}, serverName string) error
}

// GooseExtension represents an extension in Goose's config.yaml format
type GooseExtension struct {
	Name    string `yaml:"name"`
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"`
	Timeout int    `yaml:"timeout,omitempty"`
	Uri     string `yaml:"uri,omitempty"`
}

// GooseConfig represents the structure of Goose's config.yaml
type GooseConfig struct {
	Extensions map[string]GooseExtension `yaml:"extensions"`
}

// GooseYAMLConverter implements YAMLConverter for Goose config format
type GooseYAMLConverter struct{}

// ConvertFromMCPServer converts an MCPServer to a GooseExtension
func (*GooseYAMLConverter) ConvertFromMCPServer(serverName string, server MCPServer) (interface{}, error) {
	uri := server.Url
	if server.ServerUrl != "" {
		uri = server.ServerUrl
	}

	return GooseExtension{
		Name:    serverName,
		Enabled: true,
		Timeout: GooseTimeout,
		Type:    server.Type,
		Uri:     uri,
	}, nil
}

// UpsertEntry adds or updates a server entry in the config map
func (*GooseYAMLConverter) UpsertEntry(config interface{}, serverName string, entry interface{}) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	// Handle generic map - preserve all existing fields
	extension, ok := entry.(GooseExtension)
	if !ok {
		return fmt.Errorf("entry is not a GooseExtension")
	}

	if configMap["extensions"] == nil {
		configMap["extensions"] = make(map[string]interface{})
	}

	extensions, ok := configMap["extensions"].(map[string]interface{})
	if !ok {
		// Convert if it's a different map type
		extensions = make(map[string]interface{})
		configMap["extensions"] = extensions
	}

	// Convert GooseExtension to map for YAML marshaling
	extensionMap := map[string]interface{}{
		"name":    extension.Name,
		"enabled": extension.Enabled,
		"type":    extension.Type,
		"uri":     extension.Uri,
	}
	if extension.Timeout > 0 {
		extensionMap["timeout"] = extension.Timeout
	}

	extensions[serverName] = extensionMap
	return nil
}

// RemoveEntry removes a server entry from the config map
func (*GooseYAMLConverter) RemoveEntry(config interface{}, serverName string) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	// Handle generic map - preserve all existing fields
	if configMap["extensions"] == nil {
		return nil // Nothing to remove
	}

	extensions, ok := configMap["extensions"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid extensions format")
	}

	delete(extensions, serverName)
	return nil
}

// ContinueServer represents a server in Continue's config.yaml format
type ContinueServer struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Url  string `yaml:"url"`
}

// ContinueConfig represents the structure of Continue's config.yaml
type ContinueConfig struct {
	McpServers []ContinueServer `yaml:"mcpServers"`
}

// ContinueYAMLConverter implements YAMLConverter for Continue config format
type ContinueYAMLConverter struct{}

// ConvertFromMCPServer converts an MCPServer to a ContinueServer
func (*ContinueYAMLConverter) ConvertFromMCPServer(serverName string, server MCPServer) (interface{}, error) {
	url := server.Url
	if server.ServerUrl != "" {
		url = server.ServerUrl
	}

	return ContinueServer{
		Name: serverName,
		Type: server.Type,
		Url:  url,
	}, nil
}

// UpsertEntry adds or updates a server entry in the config array
func (*ContinueYAMLConverter) UpsertEntry(config interface{}, serverName string, entry interface{}) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	server, ok := entry.(ContinueServer)
	if !ok {
		return fmt.Errorf("entry is not a ContinueServer")
	}

	// Initialize mcpServers array if it doesn't exist
	if configMap["mcpServers"] == nil {
		configMap["mcpServers"] = []interface{}{}
	}

	// Get the servers array
	var servers []interface{}
	switch v := configMap["mcpServers"].(type) {
	case []interface{}:
		servers = v
	case []map[string]interface{}:
		// Convert to []interface{}
		servers = make([]interface{}, len(v))
		for i, s := range v {
			servers[i] = s
		}
	default:
		return fmt.Errorf("invalid mcpServers format")
	}

	// Convert ContinueServer to map for YAML marshaling
	serverMap := map[string]interface{}{
		"name": server.Name,
		"type": server.Type,
		"url":  server.Url,
	}

	// Check if server already exists and update it, or append if new
	found := false
	for i, s := range servers {
		serverEntry, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		if name, exists := serverEntry["name"]; exists && name == serverName {
			servers[i] = serverMap
			found = true
			break
		}
	}

	if !found {
		servers = append(servers, serverMap)
	}

	configMap["mcpServers"] = servers
	return nil
}

// RemoveEntry removes a server entry from the config array
func (*ContinueYAMLConverter) RemoveEntry(config interface{}, serverName string) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	if configMap["mcpServers"] == nil {
		return nil // Nothing to remove
	}

	// Get the servers array
	var servers []interface{}
	switch v := configMap["mcpServers"].(type) {
	case []interface{}:
		servers = v
	case []map[string]interface{}:
		// Convert to []interface{}
		servers = make([]interface{}, len(v))
		for i, s := range v {
			servers[i] = s
		}
	default:
		return fmt.Errorf("invalid mcpServers format")
	}

	// Filter out the server with the matching name
	filtered := make([]interface{}, 0, len(servers))
	for _, s := range servers {
		serverEntry, ok := s.(map[string]interface{})
		if !ok {
			filtered = append(filtered, s)
			continue
		}
		if name, exists := serverEntry["name"]; !exists || name != serverName {
			filtered = append(filtered, s)
		}
	}

	configMap["mcpServers"] = filtered
	return nil
}
