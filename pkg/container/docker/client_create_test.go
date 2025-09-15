package docker

import (
	"context"
	"fmt"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestCreateMcpContainer_Isolated_WiresConfigAndNetworks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var gotConfig *container.Config
	var gotHost *container.HostConfig
	var gotNet *network.NetworkingConfig
	var createCalled bool
	var startCalled bool

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, cfg *container.Config, host *container.HostConfig, netCfg *network.NetworkingConfig, _ *v1.Platform, name string) (container.CreateResponse, error) {
			createCalled = true
			gotConfig = cfg
			gotHost = host
			gotNet = netCfg
			assert.Equal(t, "app", name)
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, id string, _ container.StartOptions) error {
			startCalled = true
			assert.Equal(t, "cid-new", id)
			return nil
		},
	}
	c := &Client{api: api}

	perm := &runtime.PermissionConfig{
		Mounts: []runtime.Mount{
			{Source: "/src1", Target: "/dst1", ReadOnly: true},
		},
		NetworkMode: "bridge",
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"NET_BIND_SERVICE"},
		SecurityOpt: []string{"seccomp:unconfined"},
		Privileged:  false,
	}
	env := map[string]string{"A": "a", "B": "b"}
	labels := map[string]string{"toolhive": "true", "name": "app"}
	exposed := map[string]struct{}{"8080/tcp": {}}
	bindings := map[string][]runtime.PortBinding{
		"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "18080"}},
	}

	err := c.createMcpContainer(
		ctx,
		"app",
		"toolhive-app-internal",
		"img",
		[]string{"serve"},
		env,
		labels,
		true,
		perm,
		"1.2.3.4", // additionalDNS
		exposed,
		bindings,
		true, // isolateNetwork
	)
	require.NoError(t, err)

	require.True(t, createCalled)
	require.True(t, startCalled)

	// Validate container.Config
	require.NotNil(t, gotConfig)
	assert.Equal(t, "img", gotConfig.Image)
	assert.Equal(t, []string{"serve"}, []string(gotConfig.Cmd))
	// Env converted to slice containing A=a and B=b (order is not guaranteed)
	envSet := map[string]struct{}{}
	for _, e := range gotConfig.Env {
		envSet[e] = struct{}{}
	}
	_, okA := envSet["A=a"]
	_, okB := envSet["B=b"]
	assert.True(t, okA && okB, "expected Env to contain A=a and B=b, got %v", gotConfig.Env)
	assert.Equal(t, labels, gotConfig.Labels)

	// Exposed ports set
	p8080, err := nat.NewPort("tcp", "8080")
	require.NoError(t, err)
	require.Contains(t, gotConfig.ExposedPorts, p8080)

	// Validate HostConfig
	require.NotNil(t, gotHost)
	assert.Equal(t, container.NetworkMode("bridge"), gotHost.NetworkMode)
	assert.Equal(t, []string{"NET_BIND_SERVICE"}, []string(gotHost.CapAdd))
	assert.Equal(t, []string{"ALL"}, []string(gotHost.CapDrop))
	assert.Equal(t, []string{"seccomp:unconfined"}, gotHost.SecurityOpt)
	assert.Equal(t, false, gotHost.Privileged)
	assert.Equal(t, []string{"1.2.3.4"}, gotHost.DNS)

	// Port bindings wired
	require.Contains(t, gotHost.PortBindings, p8080)
	require.Len(t, gotHost.PortBindings[p8080], 1)
	assert.Equal(t, "127.0.0.1", gotHost.PortBindings[p8080][0].HostIP)
	assert.Equal(t, "18080", gotHost.PortBindings[p8080][0].HostPort)

	// Networking config points to internal network when isolated
	require.NotNil(t, gotNet)
	require.Contains(t, gotNet.EndpointsConfig, "toolhive-app-internal")
}

func TestCreateMcpContainer_NonIsolated_UsesExternalNetwork(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var gotNet *network.NetworkingConfig

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, netCfg *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			gotNet = netCfg
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
	}
	c := &Client{api: api}

	err := c.createMcpContainer(
		ctx,
		"svc",
		"", // networkName unused when isolateNetwork=false
		"img",
		nil,
		nil,
		map[string]string{"toolhive": "true"},
		false,
		&runtime.PermissionConfig{},
		"", // no additional DNS
		map[string]struct{}{},
		map[string][]runtime.PortBinding{},
		false, // not isolated
	)
	require.NoError(t, err)
	require.NotNil(t, gotNet)
	require.Contains(t, gotNet.EndpointsConfig, "toolhive-external")
}

func TestCreateContainer_CreateAndStart_New(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var createdName string
	var gotNet *network.NetworkingConfig
	var created bool
	var started bool

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			// No existing container
			return []container.Summary{}, nil
		},
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, netCfg *network.NetworkingConfig, _ *v1.Platform, name string) (container.CreateResponse, error) {
			created = true
			createdName = name
			gotNet = netCfg
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, id string, _ container.StartOptions) error {
			started = true
			assert.Equal(t, "cid-new", id)
			return nil
		},
	}
	c := &Client{api: api}

	cfg := &container.Config{}
	hcfg := &container.HostConfig{}
	endpoints := map[string]*network.EndpointSettings{
		"n1": {NetworkID: "n1"},
	}
	id, err := c.createContainer(ctx, "new", cfg, hcfg, endpoints)
	require.NoError(t, err)
	assert.Equal(t, "cid-new", id)
	assert.True(t, created)
	assert.True(t, started)
	assert.Equal(t, "new", createdName)
	require.NotNil(t, gotNet)
	require.Contains(t, gotNet.EndpointsConfig, "n1")
}

func TestCreateContainer_ReuseExisting_WhenConfigMatchesAndStartIfStopped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var createCalled bool
	var startCalled bool

	// Desired config/hostConfig
	cfg := &container.Config{}
	hcfg := &container.HostConfig{}

	api := &fakeDockerAPI{
		// findExistingContainer will call ContainerList filtering by name
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "cid-reuse", Names: []string{"/reuse"}},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-reuse", id)
			// Existing matches desired (all zero-values) and is not running
			return container.InspectResponse{
				Config: &container.Config{},
				ContainerJSONBase: &container.ContainerJSONBase{
					HostConfig: &container.HostConfig{},
					State:      &container.State{Status: "exited", Running: false},
				},
			}, nil
		},
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			createCalled = true
			return container.CreateResponse{ID: "cid-should-not"}, nil
		},
		startFunc: func(_ context.Context, id string, _ container.StartOptions) error {
			startCalled = true
			assert.Equal(t, "cid-reuse", id)
			return nil
		},
	}
	c := &Client{api: api}

	id, err := c.createContainer(ctx, "reuse", cfg, hcfg, nil)
	require.NoError(t, err)
	assert.Equal(t, "cid-reuse", id)
	assert.False(t, createCalled, "ContainerCreate should not be called when reusing")
	assert.True(t, startCalled, "ContainerStart should be called to start stopped container")
}

func TestCreateContainer_Mismatch_RemovesAndRecreates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var removedID string
	var created bool
	var started bool

	cfg := &container.Config{Image: "desired"}
	hcfg := &container.HostConfig{}

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "cid-old", Names: []string{"/app"}},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-old", id)
			// Existing image different -> mismatch path
			return container.InspectResponse{
				Config: &container.Config{Image: "different"},
				ContainerJSONBase: &container.ContainerJSONBase{
					HostConfig: &container.HostConfig{},
					State:      &container.State{Status: "running", Running: true},
				},
			}, nil
		},
		removeFunc: func(_ context.Context, id string, _ container.RemoveOptions) error {
			removedID = id
			return nil
		},
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			created = true
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, id string, _ container.StartOptions) error {
			started = true
			assert.Equal(t, "cid-new", id)
			return nil
		},
	}
	c := &Client{api: api}

	id, err := c.createContainer(ctx, "app", cfg, hcfg, nil)
	require.NoError(t, err)
	assert.Equal(t, "cid-new", id)
	assert.Equal(t, "cid-old", removedID, "expected old container to be removed before recreation")
	assert.True(t, created)
	assert.True(t, started)
}

func TestCreateMcpContainer_InvalidExposedPort_ReturnsError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			t.Fatalf("ContainerCreate should not be called when exposed ports are invalid")
			return container.CreateResponse{}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			t.Fatalf("ContainerStart should not be called when exposed ports are invalid")
			return nil
		},
	}
	c := &Client{api: api}

	perm := &runtime.PermissionConfig{}
	labels := map[string]string{"toolhive": "true"}
	// Invalid exposed port key (non-numeric)
	exposed := map[string]struct{}{"abc/tcp": {}}

	err := c.createMcpContainer(
		ctx,
		"badports",
		"toolhive-badports-internal",
		"img",
		nil,
		nil,
		labels,
		false,
		perm,
		"",
		exposed,
		map[string][]runtime.PortBinding{},
		true,
	)
	require.Error(t, err)
}

func TestCreateMcpContainer_InvalidPortBinding_ReturnsError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			t.Fatalf("ContainerCreate should not be called when port bindings are invalid")
			return container.CreateResponse{}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			t.Fatalf("ContainerStart should not be called when port bindings are invalid")
			return nil
		},
	}
	c := &Client{api: api}

	perm := &runtime.PermissionConfig{}
	labels := map[string]string{"toolhive": "true"}
	// Invalid port binding key (non-numeric)
	bindings := map[string][]runtime.PortBinding{
		"abc/tcp": {{HostIP: "127.0.0.1", HostPort: "18080"}},
	}

	err := c.createMcpContainer(
		ctx,
		"badbindings",
		"toolhive-badbindings-internal",
		"img",
		nil,
		nil,
		labels,
		false,
		perm,
		"",
		map[string]struct{}{},
		bindings,
		true,
	)
	require.Error(t, err)
}

func TestCreateMcpContainer_NoAdditionalDNS_DNSNotSet(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	var gotHost *container.HostConfig

	api := &fakeDockerAPI{
		createFunc: func(_ context.Context, _ *container.Config, host *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			gotHost = host
			return container.CreateResponse{ID: "cid-dns"}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
	}
	c := &Client{api: api}

	err := c.createMcpContainer(
		ctx,
		"nodns",
		"", // network not used here
		"img",
		nil,
		nil,
		map[string]string{"toolhive": "true"},
		false,
		&runtime.PermissionConfig{},
		"", // no additional DNS
		map[string]struct{}{},
		map[string][]runtime.PortBinding{},
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, gotHost)
	assert.True(t, len(gotHost.DNS) == 0, "expected DNS to be empty when additionalDNS is not provided")
}

func TestCreateContainer_ListError_Propagates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return nil, fmt.Errorf("list fail")
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "x", &container.Config{}, &container.HostConfig{}, nil)
	require.Error(t, err)
}

func TestCreateContainer_InspectError_Propagates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "cid1", Names: []string{"/x"}},
			}, nil
		},
		inspectFunc: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{}, fmt.Errorf("inspect fail")
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "x", &container.Config{}, &container.HostConfig{}, nil)
	require.Error(t, err)
}

func TestCreateContainer_StartExistingError_Wrapped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "cid-exist", Names: []string{"/svc"}},
			}, nil
		},
		inspectFunc: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				Config: &container.Config{},
				ContainerJSONBase: &container.ContainerJSONBase{
					HostConfig: &container.HostConfig{},
					State:      &container.State{Status: "exited", Running: false},
				},
			}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			return fmt.Errorf("start fail")
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "svc", &container.Config{}, &container.HostConfig{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start existing container")
}

func TestCreateContainer_CreateError_Wrapped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{}, nil
		},
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{}, fmt.Errorf("create fail")
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "new", &container.Config{}, &container.HostConfig{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create container")
}

func TestCreateContainer_StartError_Wrapped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{}, nil
		},
		createFunc: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *v1.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "cid-new"}, nil
		},
		startFunc: func(_ context.Context, _ string, _ container.StartOptions) error {
			return fmt.Errorf("start fail")
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "svc", &container.Config{}, &container.HostConfig{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start container")
}

func TestCreateContainer_RemoveError_Propagates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "cid-old", Names: []string{"/svc"}},
			}, nil
		},
		inspectFunc: func(_ context.Context, _ string) (container.InspectResponse, error) {
			// Mismatch to force recreation
			return container.InspectResponse{
				Config: &container.Config{Image: "different"},
				ContainerJSONBase: &container.ContainerJSONBase{
					HostConfig: &container.HostConfig{},
					State:      &container.State{Status: "running", Running: true},
				},
			}, nil
		},
		removeFunc: func(_ context.Context, id string, _ container.RemoveOptions) error {
			return fmt.Errorf("remove fail: %s", id)
		},
	}
	c := &Client{api: api}

	_, err := c.createContainer(ctx, "svc", &container.Config{Image: "desired"}, &container.HostConfig{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to remove container")
}
