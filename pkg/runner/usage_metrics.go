// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"strings"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
)

// runtimeTyper is implemented by deployers that know which container runtime
// they are connected to (the Docker-compatible client).
type runtimeTyper interface {
	RuntimeType() rt.Type
}

// UsageMetricsProfile describes this server for anonymous usage metrics
// (implements usagemetrics.ProfileProvider). It only reports which kind of
// transport, runtime, registry source and authentication are configured; names,
// images and URLs are never included, except the entry name of a server that
// came from the built-in registry.
func (c *RunConfig) UsageMetricsProfile() usagemetrics.Profile {
	source := c.registrySource()
	return usagemetrics.Profile{
		Transport: string(c.Transport),
		ServerKind: func() string {
			if c.RemoteURL != "" {
				return usagemetrics.ServerKindRemote
			}
			return usagemetrics.ServerKindContainer
		}(),
		RegistrySource: source,
		CatalogServer: func() string {
			if source == usagemetrics.ValueBuiltin {
				return c.RegistryServerName
			}
			return ""
		}(),
		Runtime:  c.runtimeName(),
		AuthMode: c.authMode(),
		Authz: func() string {
			if c.AuthzConfig != nil || c.AuthzConfigPath != "" {
				return usagemetrics.ValueOn
			}
			return usagemetrics.ValueOff
		}(),
	}
}

// registrySource classifies where the server definition came from. RunConfig
// records the registry URLs only for remote and API registries; with both empty,
// the server came from the embedded registry or a local registry file, which
// the embedded catalog lookup tells apart. A local file entry that reuses a
// built-in name is reported as builtin: RunConfig does not record the file, and
// the name sent is one the built-in catalog already publishes.
func (c *RunConfig) registrySource() string {
	switch {
	case c.RegistryServerName == "" && c.RemoteURL == "":
		return packageBuildSource(c.Image)
	case c.RegistryServerName == "":
		return usagemetrics.ValueNone
	case c.RegistryAPIURL != "":
		return usagemetrics.RegistrySourceCustomAPI
	case c.RegistryURL != "":
		return usagemetrics.RegistrySourceCustomRemote
	case usagemetrics.IsBuiltinCatalogServer(c.RegistryServerName):
		return usagemetrics.ValueBuiltin
	default:
		return usagemetrics.RegistrySourceCustomLocal
	}
}

func (c *RunConfig) runtimeName() string {
	switch {
	case rt.IsKubernetesRuntime():
		return usagemetrics.RuntimeKubernetes
	case c.RemoteURL != "":
		return usagemetrics.ValueNone
	}
	if typer, ok := c.Deployer.(runtimeTyper); ok {
		return string(typer.RuntimeType())
	}
	return usagemetrics.ValueOther
}

func (c *RunConfig) authMode() string {
	switch {
	case c.EmbeddedAuthServerConfig != nil:
		return usagemetrics.AuthModeEmbedded
	case c.OIDCConfig != nil:
		return usagemetrics.AuthModeOIDC
	default:
		return usagemetrics.ValueNone
	}
}

// packageBuildSource classifies a container that came from no registry: an
// image thv built from a uvx://, npx:// or go:// package (named by
// generateImageName), or else a plain image. Only the package kind is
// reported, never the package or image name. An image built with an explicit
// name (thv build --tag) and run by that name counts as a plain image.
func packageBuildSource(image string) string {
	kind, _, ok := strings.Cut(strings.TrimPrefix(image, localImagePrefix), "-")
	if !ok || !strings.HasPrefix(image, localImagePrefix) {
		return usagemetrics.ValueNone
	}
	switch templates.TransportType(kind) {
	case templates.TransportTypeUVX:
		return usagemetrics.RegistrySourcePackageUVX
	case templates.TransportTypeNPX:
		return usagemetrics.RegistrySourcePackageNPX
	case templates.TransportTypeGO:
		return usagemetrics.RegistrySourcePackageGo
	default:
		return usagemetrics.ValueNone
	}
}
