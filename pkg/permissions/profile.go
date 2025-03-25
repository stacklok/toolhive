// Package permissions provides utilities for managing container permissions
// and permission profiles for the vibetool application.
package permissions

import (
	"encoding/json"
	"fmt"
	"os"
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

// NewProfile creates a new permission profile
func NewProfile() *Profile {
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

// FromFile loads a permission profile from a file
func FromFile(path string) (*Profile, error) {
	// Read the file
	// #nosec G304 - This is intentional as we're reading a user-specified permission profile
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
