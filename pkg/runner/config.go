// Package runner provides functionality for running MCP servers
package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/StacklokLabs/toolhive/pkg/auth"
	"github.com/StacklokLabs/toolhive/pkg/authz"
	"github.com/StacklokLabs/toolhive/pkg/container"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/environment"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/networking"
	"github.com/StacklokLabs/toolhive/pkg/permissions"
	"github.com/StacklokLabs/toolhive/pkg/secrets"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
)

// RunConfig contains all the configuration needed to run an MCP server
// It is serializable to JSON and YAML
type RunConfig struct {
	// Image is the Docker image to run
	Image string `json:"image" yaml:"image"`

	// CmdArgs are the arguments to pass to the container
	CmdArgs []string `json:"cmd_args,omitempty" yaml:"cmd_args,omitempty"`

	// Name is the name of the MCP server
	Name string `json:"name" yaml:"name"`

	// ContainerName is the name of the container
	ContainerName string `json:"container_name" yaml:"container_name"`

	// BaseName is the base name used for the container (without prefixes)
	BaseName string `json:"base_name" yaml:"base_name"`

	// Transport is the transport mode (sse or stdio)
	Transport types.TransportType `json:"transport" yaml:"transport"`

	// Port is the port for the HTTP proxy to listen on (host port)
	Port int `json:"port" yaml:"port"`

	// TargetPort is the port for the container to expose (only applicable to SSE transport)
	TargetPort int `json:"target_port,omitempty" yaml:"target_port,omitempty"`

	// TargetHost is the host to forward traffic to (only applicable to SSE transport)
	TargetHost string `json:"target_host,omitempty" yaml:"target_host,omitempty"`

	// PermissionProfileNameOrPath is the name or path of the permission profile
	PermissionProfileNameOrPath string `json:"permission_profile_name_or_path,omitempty" yaml:"permission_profile_name_or_path,omitempty"` //nolint:lll

	// PermissionProfile is the permission profile to use
	PermissionProfile *permissions.Profile `json:"permission_profile" yaml:"permission_profile"`

	// EnvVars are the parsed environment variables as key-value pairs
	EnvVars map[string]string `json:"env_vars,omitempty" yaml:"env_vars,omitempty"`

	// Debug indicates whether debug mode is enabled
	Debug bool `json:"debug,omitempty" yaml:"debug,omitempty"`

	// Volumes are the directory mounts to pass to the container
	// Format: "host-path:container-path[:ro]"
	Volumes []string `json:"volumes,omitempty" yaml:"volumes,omitempty"`

	// ContainerLabels are the labels to apply to the container
	ContainerLabels map[string]string `json:"container_labels,omitempty" yaml:"container_labels,omitempty"`

	// OIDCConfig contains OIDC configuration
	OIDCConfig *auth.JWTValidatorConfig `json:"oidc_config,omitempty" yaml:"oidc_config,omitempty"`

	// AuthzConfig contains the authorization configuration
	AuthzConfig *authz.Config `json:"authz_config,omitempty" yaml:"authz_config,omitempty"`

	// AuthzConfigPath is the path to the authorization configuration file
	AuthzConfigPath string `json:"authz_config_path,omitempty" yaml:"authz_config_path,omitempty"`

	// Secrets are the secret parameters to pass to the container
	// Format: "<secret name>,target=<target environment variable>"
	Secrets []string `json:"secrets,omitempty" yaml:"secrets,omitempty"`

	// Runtime is the container runtime to use (not serialized)
	Runtime rt.Runtime `json:"-" yaml:"-"`
}

// WriteJSON serializes the RunConfig to JSON and writes it to the provided writer
func (c *RunConfig) WriteJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}

// ReadJSON deserializes the RunConfig from JSON read from the provided reader
func ReadJSON(r io.Reader) (*RunConfig, error) {
	var config RunConfig
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// NewRunConfig creates a new RunConfig with default values
func NewRunConfig() *RunConfig {
	return &RunConfig{
		ContainerLabels: make(map[string]string),
		EnvVars:         make(map[string]string),
	}
}

// NewRunConfigFromFlags creates a new RunConfig with values from command-line flags
func NewRunConfigFromFlags(
	runtime rt.Runtime,
	cmdArgs []string,
	name string,
	debug bool,
	volumes []string,
	secretsList []string,
	authzConfigPath string,
	permissionProfile string,
	targetHost string,
	oidcIssuer string,
	oidcAudience string,
	oidcJwksURL string,
	oidcClientID string,
) *RunConfig {
	config := &RunConfig{
		Runtime:                     runtime,
		CmdArgs:                     cmdArgs,
		Name:                        name,
		Debug:                       debug,
		Volumes:                     volumes,
		Secrets:                     secretsList,
		AuthzConfigPath:             authzConfigPath,
		PermissionProfileNameOrPath: permissionProfile,
		TargetHost:                  targetHost,
		ContainerLabels:             make(map[string]string),
		EnvVars:                     make(map[string]string),
	}

	// Set OIDC config if any values are provided
	if oidcIssuer != "" || oidcAudience != "" || oidcJwksURL != "" || oidcClientID != "" {
		config.OIDCConfig = &auth.JWTValidatorConfig{
			Issuer:   oidcIssuer,
			Audience: oidcAudience,
			JWKSURL:  oidcJwksURL,
			ClientID: oidcClientID,
		}
	}

	return config
}

// WithAuthz adds authorization configuration to the RunConfig
func (c *RunConfig) WithAuthz(config *authz.Config) *RunConfig {
	c.AuthzConfig = config
	return c
}

// WithTransport parses and sets the transport type
func (c *RunConfig) WithTransport(transport string) (*RunConfig, error) {
	transportType, err := types.ParseTransportType(transport)
	if err != nil {
		return c, fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, stdio", transport)
	}
	c.Transport = transportType
	return c, nil
}

// WithPorts configures the host and target ports
func (c *RunConfig) WithPorts(port, targetPort int) (*RunConfig, error) {
	// Select a port for the HTTP proxy (host port)
	selectedPort, err := networking.FindOrUsePort(port)
	if err != nil {
		return c, err
	}
	logger.Log.Infof("Using host port: %d", selectedPort)
	c.Port = selectedPort

	// Select a target port for the container if using SSE transport
	if c.Transport == types.TransportTypeSSE {
		selectedTargetPort, err := networking.FindOrUsePort(targetPort)
		if err != nil {
			return c, fmt.Errorf("target port error: %w", err)
		}
		logger.Log.Infof("Using target port: %d", selectedTargetPort)
		c.TargetPort = selectedTargetPort
	}

	return c, nil
}

// ParsePermissionProfile loads and sets the permission profile
func (c *RunConfig) ParsePermissionProfile() (*RunConfig, error) {
	if c.PermissionProfileNameOrPath == "" {
		return c, fmt.Errorf("permission profile name or path is required")
	}

	var permProfile *permissions.Profile
	var err error

	switch c.PermissionProfileNameOrPath {
	case permissions.ProfileNone, "stdio":
		permProfile = permissions.BuiltinNoneProfile()
	case permissions.ProfileNetwork:
		permProfile = permissions.BuiltinNetworkProfile()
	default:
		// Try to load from file
		permProfile, err = permissions.FromFile(c.PermissionProfileNameOrPath)
		if err != nil {
			return c, fmt.Errorf("failed to load permission profile: %v", err)
		}
	}

	c.PermissionProfile = permProfile
	return c, nil
}

// WithEnvironmentVariables parses and sets environment variables
func (c *RunConfig) WithEnvironmentVariables(envVarStrings []string) (*RunConfig, error) {
	envVars, err := environment.ParseEnvironmentVariables(envVarStrings)
	if err != nil {
		return c, fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Merge the parsed environment variables with existing ones
	for key, value := range envVars {
		c.EnvVars[key] = value
	}

	// Set transport-specific environment variables
	environment.SetTransportEnvironmentVariables(c.EnvVars, string(c.Transport), c.TargetPort)
	return c, nil
}

// WithSecrets processes secrets and adds them to environment variables
func (c *RunConfig) WithSecrets(secretManager secrets.Provider) (*RunConfig, error) {
	if len(c.Secrets) == 0 {
		return c, nil // No secrets to process
	}

	secretVariables, err := environment.ParseSecretParameters(c.Secrets, secretManager)
	if err != nil {
		return c, fmt.Errorf("failed to get secrets: %v", err)
	}

	// Initialize EnvVars if it's nil
	if c.EnvVars == nil {
		c.EnvVars = make(map[string]string)
	}

	// Add secret variables to environment variables
	for key, value := range secretVariables {
		c.EnvVars[key] = value
	}

	return c, nil
}

// WithContainerName generates container name if not already set
func (c *RunConfig) WithContainerName() *RunConfig {
	if c.ContainerName == "" && c.Image != "" {
		containerName, baseName := container.GetOrGenerateContainerName(c.Name, c.Image)
		c.ContainerName = containerName
		c.BaseName = baseName
	}
	return c
}

// WithStandardLabels adds standard labels to the container
func (c *RunConfig) WithStandardLabels() *RunConfig {
	if c.ContainerLabels == nil {
		c.ContainerLabels = make(map[string]string)
	}
	// Use Name if ContainerName is not set
	containerName := c.ContainerName
	if containerName == "" {
		containerName = c.Name
	}
	labels.AddStandardLabels(c.ContainerLabels, containerName, c.BaseName, string(c.Transport), c.Port)
	return c
}

// ProcessVolumeMounts processes volume mounts and adds them to the permission profile
func (c *RunConfig) ProcessVolumeMounts() error {
	if len(c.Volumes) == 0 {
		return nil
	}

	if c.PermissionProfile == nil {
		if c.PermissionProfileNameOrPath == "" {
			return fmt.Errorf("permission profile is required when using volume mounts")
		}

		// Load the permission profile from the specified name or path
		_, err := c.ParsePermissionProfile()
		if err != nil {
			return fmt.Errorf("failed to load permission profile: %v", err)
		}
	}

	for _, volume := range c.Volumes {
		// Check if the volume has a read-only flag
		readOnly := false
		volumeSpec := volume

		if strings.HasSuffix(volume, ":ro") {
			readOnly = true
			volumeSpec = strings.TrimSuffix(volume, ":ro")
		}

		// Create a mount declaration
		mount := permissions.MountDeclaration(volumeSpec)
		source, target, err := mount.Parse()
		if err != nil {
			return fmt.Errorf("invalid volume format: %s (%v)", volume, err)
		}

		// Add the mount to the appropriate permission list
		if readOnly {
			// For read-only mounts, add to Read list
			c.PermissionProfile.Read = append(c.PermissionProfile.Read, mount)
		} else {
			// For read-write mounts, add to Write list
			c.PermissionProfile.Write = append(c.PermissionProfile.Write, mount)
		}

		logger.Log.Infof("Adding volume mount: %s -> %s (%s)",
			source, target,
			map[bool]string{true: "read-only", false: "read-write"}[readOnly])
	}

	return nil
}
