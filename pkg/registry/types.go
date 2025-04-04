// Package registry provides access to the MCP server registry
package registry

import (
	"time"

	"github.com/stacklok/toolhive/pkg/permissions"
)

// Registry represents the top-level structure of the MCP registry
type Registry struct {
	Version     string             `json:"version"`
	LastUpdated string             `json:"last_updated"`
	Servers     map[string]*Server `json:"servers"`
}

// Server represents an MCP server in the registry
type Server struct {
	Image         string               `json:"image"`
	Description   string               `json:"description"`
	Transport     string               `json:"transport"`
	Permissions   *permissions.Profile `json:"permissions"`
	Tools         []string             `json:"tools"`
	EnvVars       []*EnvVar            `json:"env_vars"`
	Args          []string             `json:"args"`
	Metadata      *Metadata            `json:"metadata"`
	RepositoryURL string               `json:"repository_url,omitempty"`
	Tags          []string             `json:"tags,omitempty"`
	DockerTags    []string             `json:"docker_tags,omitempty"`
}

// EnvVar represents an environment variable for an MCP server
type EnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

// Metadata represents metadata about an MCP server
type Metadata struct {
	Stars       int    `json:"stars"`
	Pulls       int    `json:"pulls"`
	LastUpdated string `json:"last_updated"`
}

// ParsedTime returns the LastUpdated field as a time.Time
func (m *Metadata) ParsedTime() (time.Time, error) {
	return time.Parse(time.RFC3339, m.LastUpdated)
}
