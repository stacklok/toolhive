package sources

// ToolHive Registry API response types
//
// NOTE: These types are duplicated from github.com/stacklok/toolhive-registry-server/cmd/thv-registry-api/api/v1
// to avoid circular dependency issues. Once thv-operator is moved to a separate repository,
// we can import these types directly from toolhive-registry-server.
//
// TODO: When thv-operator is extracted to its own repo, remove these duplicates and import from:
//       github.com/stacklok/toolhive-registry-server/cmd/thv-registry-api/api/v1

// RegistryInfoResponse represents the registry information response from /v0/info
type RegistryInfoResponse struct {
	Version      string `json:"version"`
	LastUpdated  string `json:"last_updated"`
	Source       string `json:"source"`
	TotalServers int    `json:"total_servers"`
}

// ServerSummaryResponse represents a server in list API responses (summary view)
type ServerSummaryResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tier        string `json:"tier"`
	Status      string `json:"status"`
	Transport   string `json:"transport"`
	ToolsCount  int    `json:"tools_count"`
}

// EnvVarDetail represents detailed environment variable information
type EnvVarDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// ServerDetailResponse represents a server in detail API responses (full view)
type ServerDetailResponse struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	Tier          string                 `json:"tier"`
	Status        string                 `json:"status"`
	Transport     string                 `json:"transport"`
	Tools         []string               `json:"tools"`
	EnvVars       []EnvVarDetail         `json:"env_vars,omitempty"`
	Permissions   map[string]interface{} `json:"permissions,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	RepositoryURL string                 `json:"repository_url,omitempty"`
	Tags          []string               `json:"tags,omitempty"`
	Args          []string               `json:"args,omitempty"`
	Volumes       map[string]interface{} `json:"volumes,omitempty"`
	Image         string                 `json:"image,omitempty"`
}

// ListServersResponse represents the servers list response from /v0/servers
type ListServersResponse struct {
	Servers []ServerSummaryResponse `json:"servers"`
	Total   int                     `json:"total"`
}
