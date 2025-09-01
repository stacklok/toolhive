// Package permissions provides utilities for managing container permissions
// and permission profiles for the toolhive application.
package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Built-in permission profile names
const (
	// ProfileNone is the name of the built-in profile with no permissions
	ProfileNone = "none"
	// ProfileNetwork is the name of the built-in profile with network permissions
	ProfileNetwork = "network"
)

// Profile represents a permission profile for a container
type Profile struct {
	// Name is the name of the profile
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Read is a list of mount declarations that the container can read from
	// These can be in the following formats:
	// - A single path: The same path will be mounted from host to container
	// - host-path:container-path: Different paths for host and container
	// - resource-uri:container-path: Mount a resource identified by URI to a container path
	Read []MountDeclaration `json:"read,omitempty" yaml:"read,omitempty"`

	// Write is a list of mount declarations that the container can write to
	// These follow the same format as Read mounts but with write permissions
	Write []MountDeclaration `json:"write,omitempty" yaml:"write,omitempty"`

	// Network defines network permissions
	Network *NetworkPermissions `json:"network,omitempty" yaml:"network,omitempty"`

	// Privileged indicates whether the container should run in privileged mode
	// When true, the container has access to all host devices and capabilities
	// Use with extreme caution as this removes most security isolation
	Privileged bool `json:"privileged,omitempty" yaml:"privileged,omitempty"`
}

// NetworkPermissions defines network permissions for a container
type NetworkPermissions struct {
	// Outbound defines outbound network permissions
	Outbound *OutboundNetworkPermissions `json:"outbound,omitempty" yaml:"outbound,omitempty"`
}

// OutboundNetworkPermissions defines outbound network permissions
type OutboundNetworkPermissions struct {
	// InsecureAllowAll allows all outbound network connections
	InsecureAllowAll bool `json:"insecure_allow_all,omitempty" yaml:"insecure_allow_all,omitempty"`

	// AllowHost is a list of allowed hosts
	AllowHost []string `json:"allow_host,omitempty" yaml:"allow_host,omitempty"`

	// AllowPort is a list of allowed ports
	AllowPort []int `json:"allow_port,omitempty" yaml:"allow_port,omitempty"`
}

// NewProfile creates a new permission profile
func NewProfile() *Profile {
	return &Profile{
		Name:  ProfileNone,
		Read:  []MountDeclaration{},
		Write: []MountDeclaration{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
		Privileged: false,
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

// BuiltinNoneProfile returns the built-in profile with no permissions
func BuiltinNoneProfile() *Profile {
	return &Profile{
		Name:  ProfileNone,
		Read:  []MountDeclaration{},
		Write: []MountDeclaration{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
		Privileged: false,
	}
}

// BuiltinNetworkProfile returns the built-in network profile
func BuiltinNetworkProfile() *Profile {
	return &Profile{
		Name:  ProfileNetwork,
		Read:  []MountDeclaration{},
		Write: []MountDeclaration{},
		Network: &NetworkPermissions{
			Outbound: &OutboundNetworkPermissions{
				InsecureAllowAll: true,
				AllowHost:        []string{},
				AllowPort:        []int{},
			},
		},
		Privileged: false,
	}
}

// MountDeclaration represents a mount declaration for a container
// It can be in one of the following formats:
//   - A single path: The same path will be mounted from host to container
//   - host-path:container-path: Different paths for host and container
//   - resource-uri:container-path: Mount a resource identified by URI to a container path
//     (e.g., volume://name:container-path)
type MountDeclaration string

// Regular expressions for parsing mount declarations
var (
	// windowsPathRegex matches Windows-style paths with drive letters
	// Matches patterns like C:, D:, etc. at the start of a path
	windowsPathRegex = regexp.MustCompile(`^[a-zA-Z]:[/\\]`)

	// commandInjectionPattern matches common command injection patterns
	commandInjectionPattern = regexp.MustCompile(`[$&;|]|\$\(|\` + "`")
)

// validatePath checks if a path contains potentially dangerous patterns
func validatePath(path string) error {
	if commandInjectionPattern.MatchString(path) {
		return fmt.Errorf("potential command injection detected in path: %s", path)
	}

	// Check for null bytes
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("null byte detected in path: %s", path)
	}

	return nil
}

// cleanPath cleans a path using filepath.Clean
func cleanPath(path string) string {
	return filepath.Clean(path)
}

// parseResourceURI parses a resource URI format (scheme://resource:container-path)
func parseResourceURI(declaration string) (source, target string, err error) {
	// Check if it starts like a resource URI (scheme://)
	if !strings.Contains(declaration, "://") {
		return "", "", nil // Not a resource URI
	}

	// Split on :// to get scheme and remainder
	schemeParts := strings.SplitN(declaration, "://", 2)
	if len(schemeParts) != 2 {
		return "", "", nil // Not a valid resource URI format
	}

	scheme := schemeParts[0]
	remainder := schemeParts[1]

	// Validate scheme format
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`).MatchString(scheme) {
		return "", "", nil // Invalid scheme
	}

	// Find the last colon in the remainder - this should be the separator
	// between resource name and container path
	lastColonIdx := strings.LastIndex(remainder, ":")
	if lastColonIdx == -1 {
		return "", "", nil // No separator colon found
	}

	resourceName := remainder[:lastColonIdx]
	containerPath := remainder[lastColonIdx+1:]

	// Both parts should be non-empty
	if resourceName == "" || containerPath == "" {
		return "", "", nil // Invalid format
	}

	// Validate paths
	if err := validatePath(resourceName); err != nil {
		return "", "", err
	}
	if err := validatePath(containerPath); err != nil {
		return "", "", err
	}

	// Clean paths
	cleanedResource := cleanPath(resourceName)
	cleanedTarget := cleanPath(containerPath)

	return scheme + "://" + cleanedResource, cleanedTarget, nil
}

// findColonPositions returns all positions of colons in the string
func findColonPositions(s string) []int {
	positions := []int{}
	for i, r := range s {
		if r == ':' {
			positions = append(positions, i)
		}
	}
	return positions
}

// parseWindowsPath handles Windows-style path parsing
func parseWindowsPath(declaration string, colonPositions []int) (source, target string, err error) {
	// If there's only one colon and it's at position 1 (drive letter),
	// treat this as a single path
	if len(colonPositions) == 1 && colonPositions[0] == 1 {
		if err := validatePath(declaration); err != nil {
			return "", "", err
		}
		cleanedPath := cleanPath(declaration)
		return cleanedPath, cleanedPath, nil
	}

	// If there are exactly two colons, and the first is at position 1 (drive letter),
	// then the second one should be the separator
	if len(colonPositions) == 2 && colonPositions[0] == 1 {
		hostPath := declaration[:colonPositions[1]]
		containerPath := declaration[colonPositions[1]+1:]

		if err := validatePath(hostPath); err != nil {
			return "", "", err
		}
		if err := validatePath(containerPath); err != nil {
			return "", "", err
		}

		cleanedSource := cleanPath(hostPath)
		cleanedTarget := cleanPath(containerPath)
		return cleanedSource, cleanedTarget, nil
	}

	// If there are more than 2 colons and the first is at position 1,
	// this is ambiguous and should be treated as invalid
	if len(colonPositions) > 2 && colonPositions[0] == 1 {
		return "", "", fmt.Errorf("invalid mount declaration format: %s "+
			"(Windows paths with multiple colons are ambiguous)", declaration)
	}

	return "", "", nil // Not handled by Windows path logic
}

// parseHostContainerPath handles host:container path parsing for non-Windows paths
func parseHostContainerPath(declaration string, colonPositions []int) (source, target string, err error) {
	// For non-Windows paths: if there's exactly one colon, treat as host:container
	if len(colonPositions) == 1 {
		colonIdx := colonPositions[0]
		hostPath := declaration[:colonIdx]
		containerPath := declaration[colonIdx+1:]

		if err := validatePath(hostPath); err != nil {
			return "", "", err
		}
		if err := validatePath(containerPath); err != nil {
			return "", "", err
		}

		cleanedSource := cleanPath(hostPath)
		cleanedTarget := cleanPath(containerPath)
		return cleanedSource, cleanedTarget, nil
	}

	// Multiple colons in non-Windows paths are invalid
	if len(colonPositions) > 1 {
		return "", "", fmt.Errorf("invalid mount declaration format: %s "+
			"(multiple colons found, expected single colon separator)", declaration)
	}

	return "", "", nil // Not handled
}

// parseSinglePath handles single path declarations (no colons)
func parseSinglePath(declaration string) (source, target string, err error) {
	if err := validatePath(declaration); err != nil {
		return "", "", err
	}

	cleanedPath := cleanPath(declaration)
	return cleanedPath, cleanedPath, nil
}

// Parse parses a mount declaration and returns the source and target paths
// It also cleans and validates the paths
func (m MountDeclaration) Parse() (source, target string, err error) {
	declaration := string(m)

	// Check if it's a resource URI
	if source, target, err := parseResourceURI(declaration); err != nil {
		return "", "", err
	} else if source != "" {
		return source, target, nil
	}

	// Check if it contains a colon for host:container format
	if strings.Contains(declaration, ":") {
		colonPositions := findColonPositions(declaration)

		// Special case: Windows path handling
		if windowsPathRegex.MatchString(declaration) {
			if source, target, err := parseWindowsPath(declaration, colonPositions); err != nil {
				return "", "", err
			} else if source != "" {
				return source, target, nil
			}
		}

		// Handle non-Windows host:container paths
		if source, target, err := parseHostContainerPath(declaration, colonPositions); err != nil {
			return "", "", err
		} else if source != "" {
			return source, target, nil
		}
	}

	// If it doesn't contain a colon, it's a single path
	if !strings.Contains(declaration, ":") {
		return parseSinglePath(declaration)
	}

	// If we get here, the format is invalid
	return "", "", fmt.Errorf("invalid mount declaration format: %s "+
		"(expected path, host-path:container-path, or scheme://resource:container-path)", declaration)
}

// IsValid checks if the mount declaration is valid
func (m MountDeclaration) IsValid() bool {
	_, _, err := m.Parse()
	return err == nil
}

// IsResourceURI checks if the mount declaration is a resource URI format
// This only checks the format, not the security of the paths
func (m MountDeclaration) IsResourceURI() bool {
	declaration := string(m)

	// Check if it contains ://
	if !strings.Contains(declaration, "://") {
		return false
	}

	// Split on :// to get scheme and remainder
	schemeParts := strings.SplitN(declaration, "://", 2)
	if len(schemeParts) != 2 {
		return false
	}

	scheme := schemeParts[0]
	remainder := schemeParts[1]

	// Validate scheme format
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`).MatchString(scheme) {
		return false
	}

	// Find the last colon in the remainder
	lastColonIdx := strings.LastIndex(remainder, ":")
	if lastColonIdx == -1 {
		return false
	}

	resourceName := remainder[:lastColonIdx]
	containerPath := remainder[lastColonIdx+1:]

	// Both parts should be non-empty
	return resourceName != "" && containerPath != ""
}

// GetResourceType returns the resource type if the mount declaration is a resource URI
// For example, "volume://name" would return "volume"
func (m MountDeclaration) GetResourceType() (string, error) {
	if !m.IsResourceURI() {
		return "", fmt.Errorf("not a resource URI: %s", m)
	}

	declaration := string(m)

	// Split on :// to get scheme (we know it's valid because IsResourceURI passed)
	schemeParts := strings.SplitN(declaration, "://", 2)
	return schemeParts[0], nil
}

// ParseMountDeclarations parses a list of mount declarations
func ParseMountDeclarations(declarations []string) ([]MountDeclaration, error) {
	result := make([]MountDeclaration, 0, len(declarations))

	for _, declaration := range declarations {
		mount := MountDeclaration(declaration)

		// Check if the declaration is valid
		if !mount.IsValid() {
			_, _, err := mount.Parse()
			return nil, fmt.Errorf("invalid mount declaration: %s (%v)", declaration, err)
		}

		result = append(result, mount)
	}

	return result, nil
}
