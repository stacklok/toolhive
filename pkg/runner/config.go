// Package runner provides functionality for running MCP servers
package runner

import (
	"encoding/json"
	"io"

	"github.com/stacklok/vibetool/pkg/auth"
	"github.com/stacklok/vibetool/pkg/authz"
	rt "github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/permissions"
	"github.com/stacklok/vibetool/pkg/transport/types"
)

// RunConfig contains all the configuration needed to run an MCP server
// It is serializable to JSON and YAML
type RunConfig struct {
	// Image is the Docker image to run
	Image string `json:"image" yaml:"image"`

	// CmdArgs are the arguments to pass to the container
	CmdArgs []string `json:"cmd_args,omitempty" yaml:"cmd_args,omitempty"`

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

	// NoClientConfig indicates whether to update client configuration files
	NoClientConfig bool `json:"no_client_config,omitempty" yaml:"no_client_config,omitempty"`

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

// NewRunConfig creates a new RunConfig with the provided parameters
func NewRunConfig(
	image string,
	cmdArgs []string,
	containerName string,
	baseName string,
	transport types.TransportType,
	port int,
	targetPort int,
	permProfile *permissions.Profile,
	envVars map[string]string,
	debug bool,
	volumes []string,
	runtime rt.Runtime,
) *RunConfig {
	return &RunConfig{
		Image:             image,
		CmdArgs:           cmdArgs,
		ContainerName:     containerName,
		BaseName:          baseName,
		Transport:         transport,
		Port:              port,
		TargetPort:        targetPort,
		PermissionProfile: permProfile,
		EnvVars:           envVars,
		Debug:             debug,
		Volumes:           volumes,
		ContainerLabels:   make(map[string]string),
		Runtime:           runtime,
	}
}

// WithOIDC adds OIDC configuration to the RunConfig
func (c *RunConfig) WithOIDC(issuer, audience, jwksURL, clientID string) *RunConfig {
	c.OIDCConfig = auth.NewJWTValidatorConfig(issuer, audience, jwksURL, clientID)
	return c
}

// WithAuthz adds authorization configuration to the RunConfig
func (c *RunConfig) WithAuthz(config *authz.Config) *RunConfig {
	c.AuthzConfig = config
	return c
}

// WithNoClientConfig sets the NoClientConfig flag
func (c *RunConfig) WithNoClientConfig(noClientConfig bool) *RunConfig {
	c.NoClientConfig = noClientConfig
	return c
}

// WithContainerLabels adds container labels to the RunConfig
func (c *RunConfig) WithContainerLabels(labels map[string]string) *RunConfig {
	for k, v := range labels {
		c.ContainerLabels[k] = v
	}
	return c
}
