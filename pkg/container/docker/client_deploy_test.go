package docker

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/permissions"
)

// fakeDeployOps implements deployOps for testing DeployWorkload without a live daemon.
type fakeDeployOps struct {
	// tracking flags and captured params
	externalNetworksCalled bool

	createNetworkCalls []struct {
		name     string
		internal bool
		labels   map[string]string
	}

	dnsCalled bool
	dnsID     string
	dnsIP     string

	egressCalled bool
	egressID     string

	ingressCalled bool
	ingressPort   int

	mcpCalled        bool
	mcpName          string
	mcpNetworkName   string
	mcpImage         string
	mcpCommand       []string
	mcpEnvVars       map[string]string
	mcpLabels        map[string]string
	mcpAttachStdio   bool
	mcpPermissionCfg *runtime.PermissionConfig
	mcpAdditionalDNS string
	mcpExposedPorts  map[string]struct{}
	mcpPortBindings  map[string][]runtime.PortBinding
	mcpIsolate       bool

	// error injection
	errExternalNetworks error
	errCreateNetwork    error
	errDNS              error
	errEgress           error
	errIngress          error
	errMcp              error
}

func (f *fakeDeployOps) createExternalNetworks(_ context.Context) error {
	f.externalNetworksCalled = true
	return f.errExternalNetworks
}

func (f *fakeDeployOps) createNetwork(_ context.Context, name string, labels map[string]string, internal bool) error {
	f.createNetworkCalls = append(f.createNetworkCalls, struct {
		name     string
		internal bool
		labels   map[string]string
	}{name: name, internal: internal, labels: labels})
	return f.errCreateNetwork
}

func (f *fakeDeployOps) createDnsContainer(_ context.Context, _ string, _ bool, _ string, _ map[string]*network.EndpointSettings) (string, string, error) {
	f.dnsCalled = true
	return f.dnsID, f.dnsIP, f.errDNS
}

func (f *fakeDeployOps) createEgressSquidContainer(_ context.Context, _ string, _ string, _ bool, _ map[string]struct{}, _ map[string]*network.EndpointSettings, _ *permissions.NetworkPermissions) (string, error) {
	f.egressCalled = true
	return f.egressID, f.errEgress
}

func (f *fakeDeployOps) createMcpContainer(
	_ context.Context,
	name string,
	networkName string,
	image string,
	command []string,
	envVars map[string]string,
	labels map[string]string,
	attachStdio bool,
	permissionConfig *runtime.PermissionConfig,
	additionalDNS string,
	exposedPorts map[string]struct{},
	portBindings map[string][]runtime.PortBinding,
	isolateNetwork bool,
) error {
	f.mcpCalled = true
	f.mcpName = name
	f.mcpNetworkName = networkName
	f.mcpImage = image
	f.mcpCommand = command
	f.mcpEnvVars = envVars
	f.mcpLabels = labels
	f.mcpAttachStdio = attachStdio
	f.mcpPermissionCfg = permissionConfig
	f.mcpAdditionalDNS = additionalDNS
	f.mcpExposedPorts = exposedPorts
	f.mcpPortBindings = portBindings
	f.mcpIsolate = isolateNetwork
	return f.errMcp
}

func (f *fakeDeployOps) createIngressContainer(_ context.Context, _ string, _ int, _ bool, _ map[string]*network.EndpointSettings, _ *permissions.NetworkPermissions) (int, error) {
	f.ingressCalled = true
	if f.errIngress != nil {
		return 0, f.errIngress
	}
	return f.ingressPort, nil
}

// newClientWithOps creates a minimal client with the provided ops and a fake dockerAPI.
func newClientWithOps(ops deployOps) *Client {
	return &Client{
		api: opsToFakeDockerAPI(),
		ops: ops,
	}
}

// opsToFakeDockerAPI returns a fake dockerAPI that won't be used by DeployWorkload tests directly.
func opsToFakeDockerAPI() dockerAPI {
	return &fakeDockerAPI{}
}

func TestDeployWorkload_Stdio_IsolatedNetwork_SkipsIngressAndSetsEgressEnv(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{
		dnsIP:       "172.18.0.10",
		ingressPort: 18080, // should be ignored for stdio
	}
	c := newClientWithOps(fops)

	opts := runtime.NewDeployWorkloadOptions()
	opts.AttachStdio = true
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "127.0.0.1", HostPort: "12345"},
		},
	}

	labels := map[string]string{}
	env := map[string]string{"EXISTING": "1"}

	hostPort, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"app",
		[]string{"serve"},
		env,
		labels,
		&permissions.Profile{}, // empty profile
		"stdio",
		opts,
		true, // isolateNetwork
	)
	require.NoError(t, err)

	// stdio path returns 0 and skips ingress
	assert.Equal(t, 0, hostPort)
	assert.True(t, fops.externalNetworksCalled)
	require.Len(t, fops.createNetworkCalls, 1)
	assert.True(t, fops.createNetworkCalls[0].internal)
	assert.True(t, fops.dnsCalled)
	assert.True(t, fops.egressCalled)
	assert.False(t, fops.ingressCalled)

	// MCP container created with egress env vars present
	require.True(t, fops.mcpCalled)
	require.NotNil(t, fops.mcpEnvVars)
	assert.Equal(t, "http://app-egress:3128", fops.mcpEnvVars["HTTP_PROXY"])
	assert.Equal(t, "http://app-egress:3128", fops.mcpEnvVars["HTTPS_PROXY"])
	assert.Equal(t, "localhost,127.0.0.1,::1", fops.mcpEnvVars["NO_PROXY"])

	// Network isolation label should be set on labels map
	assert.True(t, lb.HasNetworkIsolation(labels), "expected network isolation label to be set")

	// SELinux labeling should be disabled
	assert.Contains(t, fops.mcpPermissionCfg.SecurityOpt, "label:disable", "expected SELinux labeling to be disabled")

	// TODO: Test for disabled SELinux labeling in the rest of workload containers
}

func TestDeployWorkload_SSE_IsolatedNetwork_ReturnsIngressPortAndPassesDNS(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{
		dnsIP:       "172.18.0.20",
		ingressPort: 18081,
	}
	c := newClientWithOps(fops)

	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "127.0.0.1", HostPort: ""}, // random/non-deterministic is fine; will be overridden by ingress
		},
	}

	labels := map[string]string{}

	hostPort, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"svc",
		[]string{"serve"},
		nil,
		labels,
		&permissions.Profile{},
		"sse",
		opts,
		true, // isolateNetwork
	)
	require.NoError(t, err)

	// For non-stdio with network isolation, returned port comes from ingress proxy
	assert.Equal(t, 18081, hostPort)
	assert.True(t, fops.ingressCalled)
	require.True(t, fops.mcpCalled)
	assert.Equal(t, "172.18.0.20", fops.mcpAdditionalDNS, "additionalDNS passed to MCP container should come from DNS container IP")
}

func TestDeployWorkload_NoIsolation_ReturnsPortFromBindingsAndSkipsAuxContainers(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{}
	c := newClientWithOps(fops)

	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "", HostPort: "56789"},
		},
	}

	labels := map[string]string{
		"toolhive-auxiliary": "true", // force deterministic host port passthrough
	}

	hostPort, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"noiso",
		[]string{"serve"},
		nil,
		labels,
		&permissions.Profile{},
		"sse",
		opts,
		false, // no isolation
	)
	require.NoError(t, err)

	// Should not create internal network, DNS, egress, or ingress
	assert.False(t, fops.dnsCalled)
	assert.False(t, fops.egressCalled)
	assert.False(t, fops.ingressCalled)
	assert.Empty(t, fops.createNetworkCalls, "internal network should not be created when isolation is disabled")

	// MCP should be created on default network (empty name)
	require.True(t, fops.mcpCalled)
	assert.Equal(t, "", fops.mcpNetworkName)

	// Returned host port should be the one from the binding (since auxiliary retains host port)
	assert.Equal(t, 56789, hostPort)
}

func TestDeployWorkload_UnsupportedTransport_PropagatesError(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{}
	c := newClientWithOps(fops)

	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "", HostPort: "12345"},
		},
	}

	_, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"bad",
		[]string{"serve"},
		nil,
		map[string]string{},
		&permissions.Profile{},
		"invalid-transport",
		opts,
		false,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport type")
}
