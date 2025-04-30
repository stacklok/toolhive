// Package convert provides conversion functions between API types and internal types.
package convert

import (
	"github.com/StacklokLabs/toolhive/pkg/api/v1"
	"github.com/StacklokLabs/toolhive/pkg/permissions"
)

// PermissionProfileFromInternal converts a permissions.Profile to an API v1.PermissionProfile.
func PermissionProfileFromInternal(profile *permissions.Profile) *v1.PermissionProfile {
	if profile == nil {
		return nil
	}

	// Convert Read and Write mount declarations to strings
	readMounts := make([]string, len(profile.Read))
	for i, mount := range profile.Read {
		readMounts[i] = string(mount)
	}

	writeMounts := make([]string, len(profile.Write))
	for i, mount := range profile.Write {
		writeMounts[i] = string(mount)
	}

	result := &v1.PermissionProfile{
		Read:  readMounts,
		Write: writeMounts,
	}

	// Convert Network permissions if present
	if profile.Network != nil {
		result.Network = &v1.NetworkPermissions{
			Outbound: nil,
		}

		if profile.Network.Outbound != nil {
			result.Network.Outbound = &v1.OutboundNetworkPermissions{
				InsecureAllowAll: profile.Network.Outbound.InsecureAllowAll,
				AllowTransport:   profile.Network.Outbound.AllowTransport,
				AllowHost:        profile.Network.Outbound.AllowHost,
				AllowPort:        profile.Network.Outbound.AllowPort,
			}
		}
	}

	return result
}

// PermissionProfileToInternal converts an API v1.PermissionProfile to a permissions.Profile.
func PermissionProfileToInternal(profile *v1.PermissionProfile) *permissions.Profile {
	if profile == nil {
		return nil
	}

	// Convert Read and Write strings to mount declarations
	readMounts := make([]permissions.MountDeclaration, len(profile.Read))
	for i, mount := range profile.Read {
		readMounts[i] = permissions.MountDeclaration(mount)
	}

	writeMounts := make([]permissions.MountDeclaration, len(profile.Write))
	for i, mount := range profile.Write {
		writeMounts[i] = permissions.MountDeclaration(mount)
	}

	result := &permissions.Profile{
		Read:  readMounts,
		Write: writeMounts,
	}

	// Convert Network permissions if present
	if profile.Network != nil {
		result.Network = &permissions.NetworkPermissions{
			Outbound: nil,
		}

		if profile.Network.Outbound != nil {
			result.Network.Outbound = &permissions.OutboundNetworkPermissions{
				InsecureAllowAll: profile.Network.Outbound.InsecureAllowAll,
				AllowTransport:   profile.Network.Outbound.AllowTransport,
				AllowHost:        profile.Network.Outbound.AllowHost,
				AllowPort:        profile.Network.Outbound.AllowPort,
			}
		}
	}

	return result
}
