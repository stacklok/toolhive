// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"
	"strings"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/StacklokLabs/toolhive/pkg/api"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/registry"
	"github.com/StacklokLabs/toolhive/pkg/runner"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
)

// Server is the local implementation of the api.ServerAPI interface.
type Server struct {
	// runtime is the container runtime to use for container operations
	runtime rt.Runtime
	// debug indicates whether debug mode is enabled
	debug bool
}

// Constants for server status
const (
	stateRunning = "running"
	stateError   = "error"
)

// NewServer creates a new local ServerAPI with the provided runtime and debug flag.
func NewServer(r rt.Runtime, debug bool) api.ServerAPI {
	return &Server{
		runtime: r,
		debug:   debug,
	}
}

// List returns a list of running MCP servers.
//
//nolint:gocyclo // This function is complex but manageable
func (s *Server) List(ctx context.Context, opts *api.ListOptions) ([]*api.Server, error) {
	// List containers
	containers, err := s.runtime.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var toolHiveContainers []rt.ContainerInfo
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) {
			toolHiveContainers = append(toolHiveContainers, c)
		}
	}

	// Filter containers based on options
	if opts != nil {
		// Filter by status if specified
		if opts.Status != "" && opts.Status != "all" {
			var filteredContainers []rt.ContainerInfo
			for _, c := range toolHiveContainers {
				if (opts.Status == stateRunning && c.State == stateRunning) ||
					(opts.Status == "stopped" && c.State != stateRunning) {
					filteredContainers = append(filteredContainers, c)
				}
			}
			toolHiveContainers = filteredContainers
		}

		// Filter by search term if specified
		if opts.Search != "" {
			var filteredContainers []rt.ContainerInfo
			for _, c := range toolHiveContainers {
				// Search in name
				name := labels.GetContainerName(c.Labels)
				if containsIgnoreCase(name, opts.Search) {
					filteredContainers = append(filteredContainers, c)
				}
			}
			toolHiveContainers = filteredContainers
		}
	}

	// Convert to API servers
	var servers []*api.Server
	for _, c := range toolHiveContainers {
		server := containerToServer(c)
		servers = append(servers, server)
	}

	return servers, nil
}

// Get returns information about a specific running MCP server.
func (s *Server) Get(ctx context.Context, name string) (*api.Server, error) {
	// List containers
	containers, err := s.runtime.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the specified name
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) {
			containerName := labels.GetContainerName(c.Labels)
			if containerName == name || c.Name == name {
				return containerToServer(c), nil
			}
		}
	}

	return nil, fmt.Errorf("server not found: %s", name)
}

// Run runs an MCP server with the provided options.
//
//nolint:gocyclo // This function is complex but manageable
func (s *Server) Run(ctx context.Context, serverOrImage string, opts *api.RunOptions) (*api.Server, error) {
	// Convert API RunOptions to runner.RunConfig
	config := opts.ToRunnerConfig()

	// Set the runtime
	config.Runtime = s.runtime

	// Set debug mode
	config.Debug = s.debug

	// Try to find the server in the registry
	var server *registry.Server
	var err error

	// Check if the serverOrImage contains a protocol scheme (uvx:// or npx://)
	// and build a Docker image for it if needed
	processedImage, err := s.handleProtocolScheme(ctx, serverOrImage)
	if err != nil {
		return nil, fmt.Errorf("failed to process protocol scheme: %v", err)
	}

	// Update serverOrImage with the processed image if it was changed
	if processedImage != serverOrImage {
		s.logDebug("Using built image: %s instead of %s", processedImage, serverOrImage)
		serverOrImage = processedImage
		// If we processed a protocol scheme, we don't need to look up in the registry
		err = fmt.Errorf("server not found in registry")
	} else {
		// Skip registry lookup if we're using a protocol scheme
		if !isProtocolScheme(serverOrImage) {
			server, err = registry.GetServer(serverOrImage)
		} else {
			err = fmt.Errorf("server not found in registry")
		}
	}

	// Set the image based on whether we found a registry entry
	if err == nil {
		// Server found in registry
		s.logDebug("Found server '%s' in registry", serverOrImage)

		// Apply registry settings to config
		config.Image = server.Image

		// If name is not provided, use the server name from registry
		if config.Name == "" {
			config.Name = serverOrImage
		}

		// Use registry transport if not overridden
		if config.Transport == "" {
			s.logDebug("Using registry transport: %s", server.Transport)
			config.Transport = types.TransportType(server.Transport)
		}

		// Use registry target port if not overridden and transport is SSE
		if config.TargetPort == 0 && server.Transport == "sse" && server.TargetPort > 0 {
			s.logDebug("Using registry target port: %d", server.TargetPort)
			config.TargetPort = server.TargetPort
		}

		// Prepend registry args to command-line args if available
		if len(server.Args) > 0 {
			s.logDebug("Prepending registry args: %v", server.Args)
			config.CmdArgs = append(server.Args, config.CmdArgs...)
		}

		// Set permission profile if not already set
		if config.PermissionProfile == nil && server.Permissions != nil {
			config.PermissionProfile = server.Permissions
		}

		// Process environment variables from registry
		if len(server.EnvVars) > 0 {
			for _, envVar := range server.EnvVars {
				// Check if the environment variable is already provided
				if _, exists := config.EnvVars[envVar.Name]; !exists {
					if envVar.Required {
						// For required env vars, we should prompt the user
						// But since this is an API, we'll return an error
						return nil, fmt.Errorf("required environment variable not provided: %s", envVar.Name)
					} else if envVar.Default != "" {
						// Apply default value for non-required environment variables
						if config.EnvVars == nil {
							config.EnvVars = make(map[string]string)
						}
						config.EnvVars[envVar.Name] = envVar.Default
						s.logDebug("Using default value for %s: %s", envVar.Name, envVar.Default)
					}
				}
			}
		}
	} else {
		// Server not found in registry, treat as direct image
		s.logDebug("Server '%s' not found in registry, treating as Docker image", serverOrImage)
		config.Image = serverOrImage
	}

	// Pull the image if needed
	if err := s.pullImage(ctx, config.Image); err != nil {
		return nil, fmt.Errorf("failed to retrieve or pull image: %v", err)
	}

	// Generate container name if not already set
	config.WithContainerName()

	// Add standard labels
	config.WithStandardLabels()

	// Create a Runner with the RunConfig
	mcpRunner := runner.NewRunner(config)

	// Run the MCP server
	if err := mcpRunner.Run(ctx); err != nil {
		return nil, fmt.Errorf("failed to run MCP server: %v", err)
	}

	// Get the container info for the newly created container
	containers, err := s.runtime.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the specified name
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) {
			containerName := labels.GetContainerName(c.Labels)
			if containerName == config.Name || c.Name == config.Name {
				return containerToServer(c), nil
			}
		}
	}

	return nil, fmt.Errorf("server not found after running: %s", config.Name)
}

// Logs gets logs from a running MCP server.
func (s *Server) Logs(ctx context.Context, name string, opts *api.LogsOptions) error {
	// Get the container ID for the specified name
	containerID, err := s.getContainerID(ctx, name)
	if err != nil {
		return err
	}

	// Get logs from the container
	logs, err := s.runtime.ContainerLogs(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to get container logs: %v", err)
	}

	// Write logs to the output
	if opts.Output != nil {
		_, err = fmt.Fprintln(opts.Output, logs)
		if err != nil {
			return fmt.Errorf("failed to write logs to output: %v", err)
		}
	}

	return nil
}

// Search searches for MCP servers.
func (s *Server) Search(ctx context.Context, query string, _ *api.SearchOptions) ([]*api.Server, error) {
	// Create list options with the search query
	listOpts := &api.ListOptions{
		Search: query,
	}

	// List servers with the search query
	return s.List(ctx, listOpts)
}

// Helper functions

// containerToServer converts a container info to an API server
func containerToServer(c rt.ContainerInfo) *api.Server {
	// Get container name from labels
	name := labels.GetContainerName(c.Labels)
	if name == "" {
		name = c.Name // Fallback to container name
	}

	// Get transport type from labels
	transport := labels.GetTransportType(c.Labels)
	if transport == "" {
		transport = "unknown"
	}

	// Get port from labels
	port, err := labels.GetPort(c.Labels)
	if err != nil {
		port = 0
	}

	// Create a new API server
	server := &api.Server{
		Name:        name,
		Image:       c.Image,
		Transport:   transport,
		TargetPort:  port,
		ContainerID: c.ID,
		HostPort:    port,
	}

	// Set status based on container state
	switch c.State {
	case stateRunning:
		server.Status = api.ServerStatusRunning
	case stateError:
		server.Status = api.ServerStatusError
	default:
		server.Status = api.ServerStatusStopped
	}

	// Set started time if available
	if !c.Created.IsZero() {
		startedAt := c.Created
		server.StartedAt = &startedAt
	}

	return server
}

// getContainerID gets the container ID for the specified name
func (s *Server) getContainerID(ctx context.Context, name string) (string, error) {
	// List containers
	containers, err := s.runtime.ListContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the specified name or ID prefix
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) {
			containerName := labels.GetContainerName(c.Labels)
			if containerName == name || c.Name == name || strings.HasPrefix(c.ID, name) {
				return c.ID, nil
			}
		}
	}

	return "", fmt.Errorf("server not found: %s", name)
}

// getContainerInfo gets the container info for the specified name
func (s *Server) getContainerInfo(ctx context.Context, name string) (rt.ContainerInfo, error) {
	// List containers
	containers, err := s.runtime.ListContainers(ctx)
	if err != nil {
		return rt.ContainerInfo{}, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the specified name or ID prefix
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) {
			containerName := labels.GetContainerName(c.Labels)
			if containerName == name || c.Name == name || strings.HasPrefix(c.ID, name) {
				return c, nil
			}
		}
	}

	return rt.ContainerInfo{}, fmt.Errorf("server not found: %s", name)
}

// pullImage pulls an image from a remote registry if it has the "latest" tag
// or if it doesn't exist locally
func (s *Server) pullImage(ctx context.Context, image string) error {
	// Check if the image has the "latest" tag
	isLatestTag := hasLatestTag(image)

	if isLatestTag {
		// For "latest" tag, try to pull first
		logger.Log.Infof("Image %s has 'latest' tag, pulling to ensure we have the most recent version...", image)
		err := s.runtime.PullImage(ctx, image)
		if err != nil {
			// Pull failed, check if it exists locally
			logger.Log.Infof("Pull failed, checking if image exists locally: %s", image)
			imageExists, checkErr := s.runtime.ImageExists(ctx, image)
			if checkErr != nil {
				return fmt.Errorf("failed to check if image exists: %v", checkErr)
			}

			if imageExists {
				logger.Log.Debugf("Using existing local image: %s", image)
			} else {
				return fmt.Errorf("failed to pull image from remote registry and image doesn't exist locally. %v", err)
			}
		} else {
			logger.Log.Infof("Successfully pulled image: %s", image)
		}
	} else {
		// For non-latest tags, check locally first
		logger.Log.Debugf("Checking if image exists locally: %s", image)
		imageExists, err := s.runtime.ImageExists(ctx, image)
		logger.Log.Debugf("ImageExists locally: %t", imageExists)
		if err != nil {
			return fmt.Errorf("failed to check if image exists locally: %v", err)
		}

		if imageExists {
			logger.Log.Debugf("Using existing local image: %s", image)
		} else {
			// Image doesn't exist locally, try to pull
			logger.Log.Infof("Image %s not found locally, pulling...", image)
			if err := s.runtime.PullImage(ctx, image); err != nil {
				return fmt.Errorf("failed to pull image: %v", err)
			}
			logger.Log.Infof("Successfully pulled image: %s", image)
		}
	}

	return nil
}

// logDebug logs a message if debug mode is enabled
func (s *Server) logDebug(format string, args ...interface{}) {
	if s.debug {
		logger.Log.Infof(format, args...)
	}
}

// containsIgnoreCase checks if a string contains another string, ignoring case
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// hasLatestTag checks if the given image reference has the "latest" tag or no tag (which defaults to "latest")
func hasLatestTag(imageRef string) bool {
	ref, err := nameref.ParseReference(imageRef)
	if err != nil {
		// If we can't parse the reference, assume it's not "latest"
		logger.Log.Warnf("Warning: Failed to parse image reference: %v", err)
		return false
	}

	// Check if the reference is a tag
	if taggedRef, ok := ref.(nameref.Tag); ok {
		// Check if the tag is "latest"
		return taggedRef.TagStr() == "latest"
	}

	// If the reference is not a tag (e.g., it's a digest), it's not "latest"
	// If no tag was specified, it defaults to "latest"
	_, isDigest := ref.(nameref.Digest)
	return !isDigest
}
