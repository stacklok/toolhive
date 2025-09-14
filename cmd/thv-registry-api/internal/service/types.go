package service

// DeployedServer represents a deployed MCP server in Kubernetes
type DeployedServer struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Status      string `json:"status"`
	Image       string `json:"image"`
	Transport   string `json:"transport"`
	Ready       bool   `json:"ready"`
	EndpointURL string `json:"endpoint_url,omitempty"`
}
