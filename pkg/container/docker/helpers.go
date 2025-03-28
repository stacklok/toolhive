// Package docker provides Docker-specific implementation of container runtime.
package docker

import (
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/go-connections/nat"

	"github.com/stacklok/vibetool/pkg/container/runtime"
)

// convertEnvVars converts a map of environment variables to a slice of strings
// in the format "KEY=VALUE" that Docker expects.
//
// Parameters:
//   - envVars: Map of environment variable names to values
//
// Returns:
//   - Slice of strings in "KEY=VALUE" format
func convertEnvVars(envVars map[string]string) []string {
	env := make([]string, 0, len(envVars))
	for k, v := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// convertMounts converts our internal mount format to Docker's mount format.
// This ensures consistent mount handling across different container runtimes.
//
// Parameters:
//   - mounts: Slice of internal mount configurations
//
// Returns:
//   - Slice of Docker mount configurations
func convertMounts(mounts []runtime.Mount) []mount.Mount {
	result := make([]mount.Mount, 0, len(mounts))
	for _, m := range mounts {
		result = append(result, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return result
}

// setupExposedPorts configures exposed ports for a container.
// This sets up which ports the container will listen on internally.
//
// Parameters:
//   - config: Docker container configuration to modify
//   - exposedPorts: Map of port specifications to empty structs
//
// Returns:
//   - Error if port parsing fails
func setupExposedPorts(config *container.Config, exposedPorts map[string]struct{}) error {
	if len(exposedPorts) == 0 {
		return nil
	}

	config.ExposedPorts = nat.PortSet{}
	for port := range exposedPorts {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %v", err)
		}
		config.ExposedPorts[natPort] = struct{}{}
	}

	return nil
}

// setupPortBindings configures port bindings for a container.
// This maps container ports to host ports, allowing external access.
//
// Parameters:
//   - hostConfig: Docker host configuration to modify
//   - portBindings: Map of container ports to host binding specifications
//
// Returns:
//   - Error if port parsing fails
func setupPortBindings(hostConfig *container.HostConfig, portBindings map[string][]runtime.PortBinding) error {
	if len(portBindings) == 0 {
		return nil
	}

	hostConfig.PortBindings = nat.PortMap{}
	for port, bindings := range portBindings {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return fmt.Errorf("failed to parse port: %v", err)
		}

		natBindings := make([]nat.PortBinding, len(bindings))
		for i, binding := range bindings {
			natBindings[i] = nat.PortBinding{
				HostIP:   binding.HostIP,
				HostPort: binding.HostPort,
			}
		}
		hostConfig.PortBindings[natPort] = natBindings
	}

	return nil
}

// compareStringSlices compares two string slices for equality, ignoring order.
// This is used for comparing commands, capabilities, and security options.
//
// Parameters:
//   - a, b: String slices to compare
//
// Returns:
//   - true if slices contain the same elements, false otherwise
func compareStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]struct{}, len(a))
	for _, s := range a {
		aMap[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := aMap[s]; !ok {
			return false
		}
	}
	return true
}

// compareMounts compares Docker mount configurations with our internal mount format.
// This ensures that container recreation uses identical mount points.
//
// Parameters:
//   - actual: Docker mount configurations
//   - expected: Internal mount configurations
//
// Returns:
//   - true if mount configurations match, false otherwise
func compareMounts(actual []container.MountPoint, expected []runtime.Mount) bool {
	if len(actual) != len(expected) {
		return false
	}

	// Create maps for easier comparison
	actualMap := make(map[string]container.MountPoint)
	for _, m := range actual {
		actualMap[m.Destination] = m
	}

	// Compare each expected mount with its actual counterpart
	for _, expectedMount := range expected {
		actualMount, exists := actualMap[expectedMount.Target]
		if !exists {
			return false
		}

		// Compare mount properties
		if actualMount.Source != expectedMount.Source ||
			actualMount.RW == expectedMount.ReadOnly {
			return false
		}
	}

	return true
}

// compareEnvVars compares environment variables between actual and expected configurations.
// This ensures that container recreation uses identical environment variables.
//
// Parameters:
//   - actual: Current environment variables in container
//   - expected: Desired environment variables
//
// Returns:
//   - true if environment variables match, false otherwise
func compareEnvVars(actual, expected []string) bool {
	if len(actual) < len(expected) {
		return false
	}
	expectedMap := make(map[string]string, len(expected))
	for _, env := range expected {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			expectedMap[parts[0]] = parts[1]
		}
	}
	for _, env := range actual {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			if expected, ok := expectedMap[parts[0]]; ok {
				if expected != parts[1] {
					return false
				}
			}
		}
	}
	return true
}

// compareExposedPorts compares exposed ports between actual and expected configurations.
// This ensures that container recreation exposes the same ports.
//
// Parameters:
//   - actual: Current exposed ports in container
//   - expected: Desired exposed ports
//
// Returns:
//   - true if exposed ports match, false otherwise
func compareExposedPorts(actual nat.PortSet, expected map[string]struct{}) bool {
	if len(actual) != len(expected) {
		return false
	}
	for port := range expected {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return false
		}
		if _, ok := actual[natPort]; !ok {
			return false
		}
	}
	return true
}

// comparePortBindings compares port bindings between actual and expected configurations.
// This ensures that container recreation uses identical port bindings.
//
// Parameters:
//   - actual: Current port bindings in container
//   - expected: Desired port bindings
//
// Returns:
//   - true if port bindings match, false otherwise
func comparePortBindings(actual nat.PortMap, expected map[string][]runtime.PortBinding) bool {
	if len(actual) != len(expected) {
		return false
	}
	for port, bindings := range expected {
		natPort, err := nat.NewPort("tcp", strings.Split(port, "/")[0])
		if err != nil {
			return false
		}
		actualBindings, ok := actual[natPort]
		if !ok || len(actualBindings) != len(bindings) {
			return false
		}
		for i, binding := range bindings {
			if actualBindings[i].HostIP != binding.HostIP ||
				actualBindings[i].HostPort != binding.HostPort {
				return false
			}
		}
	}
	return true
}
