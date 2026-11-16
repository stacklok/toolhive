// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/usagemetrics"
)

// typedDeployer is a deployer that reports its container runtime.
type typedDeployer struct {
	*runtimemocks.MockDeployer
	runtimeType rt.Type
}

func (d typedDeployer) RuntimeType() rt.Type { return d.runtimeType }

// This test registers a package-level catalog lookup and reads the process
// environment for Kubernetes detection, so it does not run in parallel.
//
//nolint:paralleltest // Mutates the package-level usagemetrics catalog lookup.
func TestRunConfig_UsageMetricsProfile(t *testing.T) {
	usagemetrics.SetCatalogLookup(func(name string) bool { return name == "fetch" })
	t.Cleanup(func() { usagemetrics.SetCatalogLookup(func(string) bool { return false }) })

	ctrl := gomock.NewController(t)
	plainDeployer := runtimemocks.NewMockDeployer(ctrl)
	podman := typedDeployer{MockDeployer: runtimemocks.NewMockDeployer(ctrl), runtimeType: rt.TypePodman}

	// base is a stdio container server with no registry, auth or authz.
	base := func() *RunConfig {
		return &RunConfig{Transport: types.TransportTypeStdio, Deployer: podman}
	}
	baseProfile := func() usagemetrics.Profile {
		return usagemetrics.Profile{
			Transport:      "stdio",
			ServerKind:     usagemetrics.ServerKindContainer,
			RegistrySource: usagemetrics.ValueNone,
			Runtime:        "podman",
			AuthMode:       usagemetrics.ValueNone,
			Authz:          usagemetrics.ValueOff,
		}
	}

	tests := []struct {
		name   string
		modify func(*RunConfig)
		expect func(*usagemetrics.Profile)
	}{
		{
			name:   "defaults",
			modify: func(*RunConfig) {},
			expect: func(*usagemetrics.Profile) {},
		},
		{
			name:   "transport passes through",
			modify: func(c *RunConfig) { c.Transport = types.TransportTypeStreamableHTTP },
			expect: func(p *usagemetrics.Profile) { p.Transport = "streamable-http" },
		},
		{
			name:   "remote server has no runtime",
			modify: func(c *RunConfig) { c.RemoteURL = "https://mcp.example.com" },
			expect: func(p *usagemetrics.Profile) {
				p.ServerKind = usagemetrics.ServerKindRemote
				p.Runtime = usagemetrics.ValueNone
			},
		},
		{
			name:   "deployer without a runtime type",
			modify: func(c *RunConfig) { c.Deployer = plainDeployer },
			expect: func(p *usagemetrics.Profile) { p.Runtime = usagemetrics.ValueOther },
		},
		{
			name: "registry API source",
			modify: func(c *RunConfig) {
				c.RegistryServerName = "fetch"
				c.RegistryAPIURL = "https://registry.example.com"
				c.RegistryURL = "https://registry.example.com/registry.json"
			},
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourceCustomAPI },
		},
		{
			name: "remote registry source",
			modify: func(c *RunConfig) {
				c.RegistryServerName = "fetch"
				c.RegistryURL = "https://registry.example.com/registry.json"
			},
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourceCustomRemote },
		},
		{
			name:   "builtin registry entry reports its name",
			modify: func(c *RunConfig) { c.RegistryServerName = "fetch" },
			expect: func(p *usagemetrics.Profile) {
				p.RegistrySource = usagemetrics.ValueBuiltin
				p.CatalogServer = "fetch"
			},
		},
		{
			name:   "image built from a uvx package",
			modify: func(c *RunConfig) { c.Image = generateImageName(templates.TransportTypeUVX, "mcp-server-fetch") },
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourcePackageUVX },
		},
		{
			name:   "image built from an npx package",
			modify: func(c *RunConfig) { c.Image = generateImageName(templates.TransportTypeNPX, "@scope/server") },
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourcePackageNPX },
		},
		{
			name:   "image built from a go package",
			modify: func(c *RunConfig) { c.Image = generateImageName(templates.TransportTypeGO, "github.com/x/server") },
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourcePackageGo },
		},
		{
			name:   "plain image",
			modify: func(c *RunConfig) { c.Image = "ghcr.io/example/server:1.0" },
			expect: func(*usagemetrics.Profile) {},
		},
		{
			name:   "local image with an unknown kind",
			modify: func(c *RunConfig) { c.Image = "toolhivelocal/custom:latest" },
			expect: func(*usagemetrics.Profile) {},
		},
		{
			name:   "local registry entry not in the catalog",
			modify: func(c *RunConfig) { c.RegistryServerName = "my-private-server" },
			expect: func(p *usagemetrics.Profile) { p.RegistrySource = usagemetrics.RegistrySourceCustomLocal },
		},
		{
			name: "embedded auth server wins over OIDC",
			modify: func(c *RunConfig) {
				c.EmbeddedAuthServerConfig = &authserver.RunConfig{}
				c.OIDCConfig = &auth.TokenValidatorConfig{}
			},
			expect: func(p *usagemetrics.Profile) { p.AuthMode = usagemetrics.AuthModeEmbedded },
		},
		{
			name:   "OIDC auth",
			modify: func(c *RunConfig) { c.OIDCConfig = &auth.TokenValidatorConfig{} },
			expect: func(p *usagemetrics.Profile) { p.AuthMode = usagemetrics.AuthModeOIDC },
		},
		{
			name:   "authz from config",
			modify: func(c *RunConfig) { c.AuthzConfig = &authz.Config{} },
			expect: func(p *usagemetrics.Profile) { p.Authz = usagemetrics.ValueOn },
		},
		{
			name:   "authz from config path",
			modify: func(c *RunConfig) { c.AuthzConfigPath = "/etc/authz.json" },
			expect: func(p *usagemetrics.Profile) { p.Authz = usagemetrics.ValueOn },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.modify(cfg)
			want := baseProfile()
			tt.expect(&want)

			assert.Equal(t, want, cfg.UsageMetricsProfile())
		})
	}
}
