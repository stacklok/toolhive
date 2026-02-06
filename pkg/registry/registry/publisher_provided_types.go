// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/permissions"
)

// ServerExtensions represents the ToolHive-specific extensions for an MCP server
// in the publisher-provided metadata format.
//
// This structure is used in the _meta["io.modelcontextprotocol.registry/publisher-provided"]
// section of the upstream MCP registry format, keyed by server identifier:
//   - For container servers: keyed by OCI image reference (e.g., "ghcr.io/org/image:tag")
//   - For remote servers: keyed by URL (e.g., "https://api.example.com/mcp")
//
// Container servers may use: Status, Tier, Tools, Tags, Metadata, CustomMetadata,
// Permissions, Args, Provenance, DockerTags, ProxyPort, ToolDefinitions
//
// Remote servers may use: Status, Tier, Tools, Tags, Metadata, CustomMetadata,
// OAuthConfig, EnvVars, ToolDefinitions
type ServerExtensions struct {
	// Status indicates whether the server is active or deprecated (required)
	Status string `json:"status" yaml:"status"`
	// Tier represents the classification level (e.g., "Official", "Community")
	Tier string `json:"tier,omitempty" yaml:"tier,omitempty"`
	// Tools lists the tool names provided by this MCP server
	Tools []string `json:"tools,omitempty" yaml:"tools,omitempty"`
	// Tags are categorization labels for search and filtering
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// Metadata contains popularity metrics and optional Kubernetes-specific metadata.
	// The Kubernetes metadata is only populated when:
	// - The server is served from ToolHive Registry Server
	// - The server was auto-discovered from a Kubernetes deployment
	// - The Kubernetes resource has the required registry annotations
	Metadata *Metadata `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// CustomMetadata allows for additional user-defined metadata
	CustomMetadata map[string]any `json:"custom_metadata,omitempty" yaml:"custom_metadata,omitempty"`

	// Container-specific fields (only for servers keyed by OCI image reference)

	// Permissions defines security permissions for container-based servers
	Permissions *permissions.Profile `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	// Args are default command-line arguments for container-based servers
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Provenance contains supply chain provenance information for container-based servers
	Provenance *Provenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	// DockerTags lists available Docker tags for container-based servers
	DockerTags []string `json:"docker_tags,omitempty" yaml:"docker_tags,omitempty"`
	// ProxyPort is the HTTP proxy port for container-based servers (1-65535)
	ProxyPort int `json:"proxy_port,omitempty" yaml:"proxy_port,omitempty"`

	// Remote server-specific fields (only for servers keyed by URL)

	// OAuthConfig defines OAuth/OIDC configuration for remote servers
	OAuthConfig *OAuthConfig `json:"oauth_config,omitempty" yaml:"oauth_config,omitempty"`
	// EnvVars defines environment variables for remote server client configuration
	EnvVars []*EnvVar `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// Optional tool definitions (MCP protocol)

	// ToolDefinitions contains an array of MCP Tool definitions that describe the tools
	// available from this server. This field can be populated by:
	// - ToolHive Registry Server (extracted from Kubernetes annotations)
	// - Registry publishers (to pre-declare available tools)
	// - Any other source that wants to advertise tool capabilities
	// The array contains Tool objects as defined in the MCP specification.
	ToolDefinitions []mcp.Tool `json:"tool_definitions,omitempty" yaml:"tool_definitions,omitempty"`
}

// ToolHivePublisherNamespace is the publisher namespace used by ToolHive in the
// publisher-provided extensions: "io.github.stacklok"
const ToolHivePublisherNamespace = "io.github.stacklok"

// PublisherProvidedKey is the key used in the _meta object for publisher-provided extensions
const PublisherProvidedKey = "io.modelcontextprotocol.registry/publisher-provided"
