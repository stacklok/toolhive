package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Profile represents a permission profile for a container
type Profile struct {
	// Read is a list of paths that the container can read from
	Read []string `json:"read,omitempty"`
	
	// Write is a list of paths that the container can write to
	Write []string `json:"write,omitempty"`
	
	// Network defines network permissions
	Network *NetworkPermissions `json:"network,omitempty"`
}

// NetworkPermissions defines network permissions for a container
type NetworkPermissions struct {
	// Outbound defines outbound network permissions
	Outbound *OutboundNetworkPermissions `json:"outbound,omitempty"`
}

// OutboundNetworkPermissions defines outbound network permissions
type OutboundNetworkPermissions struct {
	// InsecureAllowAll allows all outbound network connections
	InsecureAllowAll bool `json:"insecure_allow_all,omitempty"`
	
	// AllowTransport is a list of allowed transport protocols (tcp, udp)
	AllowTransport []string `json:"allow_transport,omitempty"`
	
	// AllowHost is a list of allowed hosts
	AllowHost []string `json:"allow_host,omitempty"`
	
	// AllowPort is a list of allowed ports
	AllowPort []int `json:"allow_port,omitempty"`
}

// Mount represents a volume mount
type Mount struct {
	// Source is the source path on the host
	Source string
	
	// Target is the target path in the container
	Target string
	
	// ReadOnly indicates if the mount is read-only
	ReadOnly bool
}

// ContainerConfig represents the container configuration derived from a permission profile
type ContainerConfig struct {
	// Mounts is the list of volume mounts
	Mounts []Mount
	
	// NetworkMode is the network mode
	NetworkMode string
	
	// CapDrop is the list of capabilities to drop
	CapDrop []string
	
	// CapAdd is the list of capabilities to add
	CapAdd []string
	
	// SecurityOpt is the list of security options
	SecurityOpt []string
}

// NewProfile creates a new permission profile
func NewProfile() *Profile {
	return &Profile{
		Read:    []string{},
		Write:   []string{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowTransport:   []string{},
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
	}
}

// FromFile loads a permission profile from a file
func FromFile(path string) (*Profile, error) {
	// Read the file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read permission profile: %w", err)
	}
	
	// Parse the JSON
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse permission profile: %w", err)
	}
	
	return &profile, nil
}

// BuiltinStdioProfile returns the built-in stdio profile
func BuiltinStdioProfile() *Profile {
	return &Profile{
		Read:  []string{},
		Write: []string{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowTransport:   []string{},
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
	}
}

// BuiltinNetworkProfile returns the built-in network profile
func BuiltinNetworkProfile() *Profile {
	return &Profile{
		Read:  []string{},
		Write: []string{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: true,
				AllowTransport:   []string{},
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
	}
}

// ToContainerConfig converts a permission profile to a container configuration
func (p *Profile) ToContainerConfig() *ContainerConfig {
	config := &ContainerConfig{
		Mounts:      []Mount{},
		NetworkMode: "none",
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{},
		SecurityOpt: []string{},
	}
	
	// Add read-only mounts
	for _, path := range p.Read {
		if !filepath.IsAbs(path) {
			// Skip relative paths
			continue
		}
		
		config.Mounts = append(config.Mounts, Mount{
			Source:   path,
			Target:   path,
			ReadOnly: true,
		})
	}
	
	// Add read-write mounts
	for _, path := range p.Write {
		if !filepath.IsAbs(path) {
			// Skip relative paths
			continue
		}
		
		// Check if the path is already mounted read-only
		alreadyMounted := false
		for i, mount := range config.Mounts {
			if mount.Target == path {
				// Update the mount to be read-write
				config.Mounts[i].ReadOnly = false
				alreadyMounted = true
				break
			}
		}
		
		// If not already mounted, add a new mount
		if !alreadyMounted {
			config.Mounts = append(config.Mounts, Mount{
				Source:   path,
				Target:   path,
				ReadOnly: false,
			})
		}
	}
	
	// Configure network
	if p.Network != nil && p.Network.Outbound != nil {
		if p.Network.Outbound.InsecureAllowAll {
			config.NetworkMode = "bridge"
		}
	}
	
	return config
}

// ToContainerConfigWithTransport converts a permission profile to a container configuration
// with transport-specific settings
func (p *Profile) ToContainerConfigWithTransport(transportType string) (*ContainerConfig, error) {
	config := p.ToContainerConfig()
	
	// Add transport-specific settings
	switch transportType {
	case "sse":
		// For SSE transport, we need network access
		config.NetworkMode = "bridge"
	case "stdio":
		// For STDIO transport, we don't need network access
		// but we need to ensure the container has access to stdin/stdout
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}
	
	return config, nil
}