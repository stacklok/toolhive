package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/permissions"
)

func TestCreateSquidContainer_Basics(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var gotHost *container.HostConfig

	var createCalled bool
	var startCalled bool

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, _ *container.Config, host *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			createCalled = true
			gotHost = host
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, id string, _ container.StartOptions) error {
			startCalled = true
			assert.Equal(t, "cid-new", id)
			return nil
		},
	}

	c := &Client{
		api:          api,
		imageManager: &fakeImageManager{},
	}

	_, err := createSquidContainer(
		ctx,
		c,
		"squid-test",
		true,
		map[string]struct{}{},
		map[string]*network.EndpointSettings{},
		map[string][]runtime.PortBinding{},
		"/tmp/squid.conf",
	)

	require.NoError(t, err)

	require.True(t, createCalled)
	require.True(t, startCalled)

	// Validate HostConfig
	require.NotNil(t, gotHost)
	assert.Equal(t, gotHost.NetworkMode, container.NetworkMode("bridge"))
	assert.ElementsMatch(t, gotHost.Mounts, []mount.Mount{
		{
			Type:     mount.TypeBind,
			Source:   "/tmp/squid.conf",
			Target:   "/etc/squid/squid.conf",
			ReadOnly: true,
		},
	})
	assert.ElementsMatch(t, gotHost.CapAdd, []string{"CAP_SETUID", "CAP_SETGID"})
	assert.Nil(t, gotHost.CapDrop)
	assert.Contains(t, gotHost.SecurityOpt, "label:disable")
	assert.Equal(t, gotHost.RestartPolicy, container.RestartPolicy{
		Name: "unless-stopped",
	})
	// TODO: Validate exposed ports & port bindings
}

func TestCreateTempEgressSquidConf_AllowAllWhenNil(t *testing.T) {
	t.Parallel()

	fp, err := createTempEgressSquidConf(nil, "server")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp) })

	b, err := os.ReadFile(fp)
	require.NoError(t, err)
	s := string(b)

	assert.Contains(t, s, "visible_hostname server-egress")
	assert.Contains(t, s, "http_port 3128")
	assert.Contains(t, s, "http_access allow all")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(s), "http_access deny all"))

	info, err := os.Stat(fp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestCreateTempEgressSquidConf_AllowAllWhenInsecure(t *testing.T) {
	t.Parallel()

	cfg := &permissions.NetworkPermissions{
		Outbound: &permissions.OutboundNetworkPermissions{
			InsecureAllowAll: true,
		},
	}
	fp, err := createTempEgressSquidConf(cfg, "server")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp) })

	b, err := os.ReadFile(fp)
	require.NoError(t, err)
	s := string(b)

	assert.Contains(t, s, "visible_hostname server-egress")
	assert.Contains(t, s, "http_port 3128")
	assert.Contains(t, s, "http_access allow all")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(s), "http_access deny all"))

	info, err := os.Stat(fp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestCreateTempEgressSquidConf_WithACLs(t *testing.T) {
	t.Parallel()

	cfg := &permissions.NetworkPermissions{
		Outbound: &permissions.OutboundNetworkPermissions{
			InsecureAllowAll: false,
			AllowPort:        []int{80, 443},
			AllowHost:        []string{"example.com", "api.github.com"},
		},
	}
	fp, err := createTempEgressSquidConf(cfg, "edge")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp) })

	b, err := os.ReadFile(fp)
	require.NoError(t, err)
	s := string(b)

	assert.Contains(t, s, "visible_hostname edge-egress")
	assert.Contains(t, s, "# Define allowed ports\nacl allowed_ports port 80 443")
	assert.Contains(t, s, "# Define allowed destinations\nacl allowed_dsts dstdomain example.com api.github.com")
	assert.Contains(t, s, "\n# Define http_access rules\n")
	assert.Contains(t, s, "http_access allow allowed_ports allowed_dsts")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(s), "http_access deny all"))

	info, err := os.Stat(fp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestCreateTempIngressSquidConf_Basics(t *testing.T) {
	t.Parallel()

	fp, err := createTempIngressSquidConf("svc-example", 8080, 18080)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp) })

	b, err := os.ReadFile(fp)
	require.NoError(t, err)
	s := string(b)

	assert.Contains(t, s, "visible_hostname svc-example-ingress")
	assert.Contains(t, s, "\n# Reverse proxy setup for port 8080\n")
	assert.Contains(t, s, "http_port 0.0.0.0:18080 accel defaultsite=svc-example")
	assert.Contains(t, s, "cache_peer svc-example parent 8080 0 no-query originserver name=origin_8080")
	assert.Contains(t, s, "acl site_8080 dstdomain svc-example")
	assert.Contains(t, s, "http_access allow site_8080")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(s), "http_access deny all"))

	info, err := os.Stat(fp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestGetSquidImage(t *testing.T) {
	t.Parallel()

	// Save and restore env
	orig, had := os.LookupEnv("TOOLHIVE_EGRESS_IMAGE")
	if had {
		t.Cleanup(func() { _ = os.Setenv("TOOLHIVE_EGRESS_IMAGE", orig) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv("TOOLHIVE_EGRESS_IMAGE") })
	}

	// Default
	_ = os.Unsetenv("TOOLHIVE_EGRESS_IMAGE")
	assert.Equal(t, "ghcr.io/stacklok/toolhive/egress-proxy:latest", getSquidImage())

	// Override
	override := "ghcr.io/example/custom-squid:1.2.3"
	_ = os.Setenv("TOOLHIVE_EGRESS_IMAGE", override)
	assert.Equal(t, override, getSquidImage())
}

// Safety: ensure generated files are written under system temp directory for cleanup logic assumptions
func TestTempFilesWrittenToSystemTempDir(t *testing.T) {
	t.Parallel()

	fp1, err := createTempEgressSquidConf(nil, "s1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp1) })

	fp2, err := createTempIngressSquidConf("s2", 8081, 18081)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(fp2) })

	tempDir := os.TempDir()
	assert.True(t, strings.HasPrefix(filepath.Clean(fp1), filepath.Clean(tempDir)))
	assert.True(t, strings.HasPrefix(filepath.Clean(fp2), filepath.Clean(tempDir)))
}
