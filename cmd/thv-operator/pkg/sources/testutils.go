package sources

import (
	"encoding/json"
	"fmt"
	"time"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// TestRegistryBuilder provides a fluent interface for building test registry data
type TestRegistryBuilder struct {
	format        string
	registry      *registry.Registry
	upstreamData  []registry.UpstreamServerDetail
	serverCounter int
}

// NewTestRegistryBuilder creates a new test registry builder for the specified format
func NewTestRegistryBuilder(format string) *TestRegistryBuilder {
	builder := &TestRegistryBuilder{
		format:        format,
		serverCounter: 1,
	}

	switch format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		builder.registry = &registry.Registry{
			Version:       "1.0.0",
			LastUpdated:   time.Now().Format(time.RFC3339),
			Servers:       make(map[string]*registry.ImageMetadata),
			RemoteServers: make(map[string]*registry.RemoteServerMetadata),
		}
	case mcpv1alpha1.RegistryFormatUpstream:
		builder.upstreamData = []registry.UpstreamServerDetail{}
	}

	return builder
}

// WithServer adds a container server with the given name and default valid values
func (b *TestRegistryBuilder) WithServer(name string) *TestRegistryBuilder {
	if name == "" {
		name = fmt.Sprintf("test-server-%d", b.serverCounter)
		b.serverCounter++
	}

	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		b.registry.Servers[name] = &registry.ImageMetadata{
			BaseServerMetadata: registry.BaseServerMetadata{
				Name:        name,
				Description: fmt.Sprintf("Test server description for %s", name),
				Tier:        "Community",
				Status:      "Active",
				Transport:   "stdio",
				Tools:       []string{"test_tool"},
			},
			Image: "test/image:latest",
		}
	case mcpv1alpha1.RegistryFormatUpstream:
		b.upstreamData = append(b.upstreamData, registry.UpstreamServerDetail{
			Server: registry.UpstreamServer{
				Name:        name,
				Description: fmt.Sprintf("Test server description for %s", name),
				Packages: []registry.UpstreamPackage{
					{
						RegistryName: "docker",
						Name:         "test/image",
						Version:      "latest",
					},
				},
			},
		})
	}

	return b
}

// WithRemoteServer adds a remote server with the given URL (only for ToolHive format)
func (b *TestRegistryBuilder) WithRemoteServer(url string) *TestRegistryBuilder {
	if b.format != mcpv1alpha1.RegistryFormatToolHive && b.format != "" {
		return b // Only supported for ToolHive format
	}

	name := fmt.Sprintf("remote-server-%d", b.serverCounter)
	b.serverCounter++

	if url == "" {
		url = fmt.Sprintf("https://remote-server-%d.example.com", b.serverCounter-1)
	}

	b.registry.RemoteServers[name] = &registry.RemoteServerMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        name,
			Description: fmt.Sprintf("Test remote server description for %s", name),
			Tier:        "Community",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"remote_tool"},
		},
		URL: url,
	}

	return b
}

// WithServerName adds a server with a specific name
func (b *TestRegistryBuilder) WithServerName(name string) *TestRegistryBuilder {
	return b.WithServer(name)
}

// WithRemoteServerName adds a remote server with a specific name and URL
func (b *TestRegistryBuilder) WithRemoteServerName(name, url string) *TestRegistryBuilder {
	if b.format != mcpv1alpha1.RegistryFormatToolHive && b.format != "" {
		return b // Only supported for ToolHive format
	}

	if url == "" {
		url = fmt.Sprintf("https://%s.example.com", name)
	}

	b.registry.RemoteServers[name] = &registry.RemoteServerMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        name,
			Description: fmt.Sprintf("Test remote server description for %s", name),
			Tier:        "Community",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"remote_tool"},
		},
		URL: url,
	}

	return b
}

// WithVersion sets a custom version (ToolHive format only)
func (b *TestRegistryBuilder) WithVersion(version string) *TestRegistryBuilder {
	if b.format == mcpv1alpha1.RegistryFormatToolHive || b.format == "" {
		b.registry.Version = version
	}
	return b
}

// WithLastUpdated sets a custom last updated timestamp (ToolHive format only)
func (b *TestRegistryBuilder) WithLastUpdated(timestamp string) *TestRegistryBuilder {
	if b.format == mcpv1alpha1.RegistryFormatToolHive || b.format == "" {
		b.registry.LastUpdated = timestamp
	}
	return b
}

// Empty creates an empty registry with minimal required fields
func (b *TestRegistryBuilder) Empty() *TestRegistryBuilder {
	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		// Keep the registry structure but clear servers
		b.registry.Servers = make(map[string]*registry.ImageMetadata)
		b.registry.RemoteServers = make(map[string]*registry.RemoteServerMetadata)
	case mcpv1alpha1.RegistryFormatUpstream:
		b.upstreamData = []registry.UpstreamServerDetail{}
	}
	return b
}

// BuildJSON returns the JSON representation of the built registry
func (b *TestRegistryBuilder) BuildJSON() []byte {
	var data []byte
	var err error

	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		data, err = json.Marshal(b.registry)
	case mcpv1alpha1.RegistryFormatUpstream:
		data, err = json.Marshal(b.upstreamData)
	}

	if err != nil {
		panic(fmt.Sprintf("Failed to marshal test data: %v", err))
	}

	return data
}

// BuildPrettyJSON returns the JSON representation with indentation for readability
func (b *TestRegistryBuilder) BuildPrettyJSON() []byte {
	var data []byte
	var err error

	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		data, err = json.MarshalIndent(b.registry, "", "  ")
	case mcpv1alpha1.RegistryFormatUpstream:
		data, err = json.MarshalIndent(b.upstreamData, "", "  ")
	}

	if err != nil {
		panic(fmt.Sprintf("Failed to marshal test data: %v", err))
	}

	return data
}

// GetRegistry returns the built registry (for ToolHive format only)
func (b *TestRegistryBuilder) GetRegistry() *registry.Registry {
	if b.format == mcpv1alpha1.RegistryFormatToolHive || b.format == "" {
		return b.registry
	}
	return nil
}

// GetUpstreamData returns the built upstream data (for Upstream format only)
func (b *TestRegistryBuilder) GetUpstreamData() []registry.UpstreamServerDetail {
	if b.format == mcpv1alpha1.RegistryFormatUpstream {
		return b.upstreamData
	}
	return nil
}

// ServerCount returns the number of servers (both container and remote for ToolHive format)
func (b *TestRegistryBuilder) ServerCount() int {
	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		return len(b.registry.Servers) + len(b.registry.RemoteServers)
	case mcpv1alpha1.RegistryFormatUpstream:
		return len(b.upstreamData)
	}
	return 0
}

// ContainerServerCount returns the number of container servers only
func (b *TestRegistryBuilder) ContainerServerCount() int {
	switch b.format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		return len(b.registry.Servers)
	case mcpv1alpha1.RegistryFormatUpstream:
		return len(b.upstreamData)
	}
	return 0
}

// RemoteServerCount returns the number of remote servers only (ToolHive format only)
func (b *TestRegistryBuilder) RemoteServerCount() int {
	if b.format == mcpv1alpha1.RegistryFormatToolHive || b.format == "" {
		return len(b.registry.RemoteServers)
	}
	return 0
}

// InvalidJSON returns intentionally malformed JSON for testing error cases
func InvalidJSON() []byte {
	return []byte("invalid json")
}

// EmptyJSON returns empty JSON object/array based on format
func EmptyJSON(format string) []byte {
	switch format {
	case mcpv1alpha1.RegistryFormatToolHive, "":
		return []byte("{}")
	case mcpv1alpha1.RegistryFormatUpstream:
		return []byte("[]")
	default:
		return []byte("{}")
	}
}
