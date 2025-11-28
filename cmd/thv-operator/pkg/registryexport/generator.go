package registryexport

import (
	"fmt"
	"sort"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/pkg/registry/registry"
)

const (
	// ToolHivePublisherDomain is the reverse-DNS domain for ToolHive operator-generated entries.
	ToolHivePublisherDomain = "dev.stacklok.toolhive"

	// DefaultVersion is the default version for generated registry entries.
	DefaultVersion = "1.0.0"
)

// Generator creates registry entries from annotated MCP resources.
type Generator struct{}

// NewGenerator creates a new Generator.
func NewGenerator() *Generator {
	return &Generator{}
}

// ExportableResource represents an MCP resource that can be exported to the registry.
type ExportableResource struct {
	// Object is the Kubernetes object being exported.
	Object client.Object
	// Transport is the MCP transport type (sse, streamable-http, stdio).
	Transport string
}

// GenerateServerEntry creates a ServerJSON from an annotated MCP resource.
// Returns nil, nil if the resource doesn't have the required registry-url annotation.
// Returns nil, error if validation fails.
func (g *Generator) GenerateServerEntry(resource ExportableResource) (*upstreamv0.ServerJSON, error) {
	annotations := resource.Object.GetAnnotations()
	if annotations == nil {
		return nil, nil
	}

	registryURL, ok := annotations[AnnotationRegistryURL]
	if !ok || registryURL == "" {
		return nil, nil
	}

	description := annotations[AnnotationRegistryDescription]
	if description == "" {
		return nil, fmt.Errorf("missing required annotation %s for resource %s/%s",
			AnnotationRegistryDescription,
			resource.Object.GetNamespace(),
			resource.Object.GetName())
	}

	serverName := g.generateServerName(resource.Object)
	if override, ok := annotations[AnnotationRegistryName]; ok && override != "" {
		serverName = override
	}

	transport := resource.Transport
	if transport == "" {
		transport = model.TransportTypeSSE
	}

	serverJSON := &upstreamv0.ServerJSON{
		Schema:      model.CurrentSchemaURL,
		Name:        serverName,
		Description: description,
		Version:     DefaultVersion,
		Remotes: []model.Transport{
			{
				Type: transport,
				URL:  registryURL,
			},
		},
	}

	return serverJSON, nil
}

// generateServerName creates a reverse-DNS server name from namespace/name.
// Format: dev.stacklok.toolhive/{namespace}.{name}
func (*Generator) generateServerName(obj client.Object) string {
	namespace := obj.GetNamespace()
	name := obj.GetName()
	return fmt.Sprintf("%s/%s.%s", ToolHivePublisherDomain, namespace, name)
}

// BuildUpstreamRegistry creates an UpstreamRegistry from a list of ServerJSON entries.
// Entries are sorted by name for deterministic output.
func (*Generator) BuildUpstreamRegistry(entries []upstreamv0.ServerJSON) *registry.UpstreamRegistry {
	// Sort entries for deterministic output
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return &registry.UpstreamRegistry{
		Schema:  "https://static.modelcontextprotocol.io/schemas/2025-10-17/registry.schema.json",
		Version: "1.0.0",
		Meta: registry.UpstreamMeta{
			LastUpdated: time.Now().UTC().Format(time.RFC3339),
		},
		Data: registry.UpstreamData{
			Servers: entries,
		},
	}
}

// HasRegistryExportAnnotation checks if an object has the registry export annotation.
func HasRegistryExportAnnotation(obj client.Object) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	_, ok := annotations[AnnotationRegistryURL]
	return ok
}
