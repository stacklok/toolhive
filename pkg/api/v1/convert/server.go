// Package convert provides conversion functions between API types and internal types.
package convert

import (
	"github.com/StacklokLabs/toolhive/pkg/api/v1"
	"github.com/StacklokLabs/toolhive/pkg/registry"
)

// ServerFromRegistry converts a registry.Server to an API v1.Server.
func ServerFromRegistry(regServer *registry.Server) *v1.Server {
	if regServer == nil {
		return nil
	}

	return &v1.Server{
		Name:          regServer.Name,
		Image:         regServer.Image,
		Description:   regServer.Description,
		Transport:     regServer.Transport,
		TargetPort:    regServer.TargetPort,
		Permissions:   PermissionProfileFromInternal(regServer.Permissions),
		Tools:         regServer.Tools,
		EnvVars:       EnvVarsFromInternal(regServer.EnvVars),
		Args:          regServer.Args,
		RepositoryURL: regServer.RepositoryURL,
		Tags:          regServer.Tags,
		DockerTags:    regServer.DockerTags,
		Status:        v1.ServerStatusStopped,
	}
}

// ServerToRegistry converts an API v1.Server to a registry.Server.
func ServerToRegistry(server *v1.Server) *registry.Server {
	if server == nil {
		return nil
	}

	return &registry.Server{
		Name:          server.Name,
		Image:         server.Image,
		Description:   server.Description,
		Transport:     server.Transport,
		TargetPort:    server.TargetPort,
		Permissions:   PermissionProfileToInternal(server.Permissions),
		Tools:         server.Tools,
		EnvVars:       EnvVarsToInternal(server.EnvVars),
		Args:          server.Args,
		RepositoryURL: server.RepositoryURL,
		Tags:          server.Tags,
		DockerTags:    server.DockerTags,
		Metadata:      &registry.Metadata{},
	}
}
