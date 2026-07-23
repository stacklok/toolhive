// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
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

	// callOrder tracks the sequence of operation calls for ordering assertions.
	callOrder *[]string

	// error injection
	errExternalNetworks error
	errCreateNetwork    error
	errDNS              error
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
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "createMcpContainer")
	}
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

// fakeNetworkProxy implements networkProxy for testing DeployWorkload without real proxy containers.
type fakeNetworkProxy struct {
	setupCalled           bool // SetupEgress was called
	ingressCalled         bool // SetupIngress was called
	transparentCalled     bool
	transparentWorkloadID string
	capturedSpec          proxySpec
	ingressPort           int // returned by SetupIngress
	err                   error
	// callOrder tracks cross-component ordering when shared with fakeDeployOps.
	callOrder *[]string
}

func (f *fakeNetworkProxy) SetupEgress(_ context.Context, spec proxySpec) (egressResult, error) {
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "SetupEgress")
	}
	f.setupCalled = true
	f.capturedSpec = spec
	if f.err != nil {
		return egressResult{}, f.err
	}
	// Return realistic env vars based on spec so MCP container env var assertions remain meaningful.
	egressContainerName := fmt.Sprintf("%s-egress", spec.WorkloadName)
	return egressResult{EnvVars: addEgressEnvVars(nil, egressContainerName)}, nil
}

func (f *fakeNetworkProxy) SetupIngress(_ context.Context, spec proxySpec, _ egressResult) (int, error) {
	if f.callOrder != nil {
		*f.callOrder = append(*f.callOrder, "SetupIngress")
	}
	f.ingressCalled = true
	f.capturedSpec = spec
	if f.err != nil {
		return 0, f.err
	}
	return f.ingressPort, nil
}

func (f *fakeNetworkProxy) SetupTransparent(_ context.Context, _ proxySpec, workloadContainerID string) error {
	f.transparentCalled = true
	f.transparentWorkloadID = workloadContainerID
	return f.err
}

// newClientWithOpsAndProxy creates a minimal client with the provided ops, proxy, and a fake dockerAPI.
func newClientWithOpsAndProxy(ops deployOps, proxy networkProxy) *Client {
	return &Client{
		api:   opsToFakeDockerAPI(),
		ops:   ops,
		proxy: proxy,
	}
}

// newClientWithOps creates a minimal client with the provided ops and a zero fakeNetworkProxy.
// Use this for tests that do not exercise isolated-network paths.
func newClientWithOps(ops deployOps) *Client {
	return newClientWithOpsAndProxy(ops, &fakeNetworkProxy{})
}

// opsToFakeDockerAPI returns a fake dockerAPI that won't be used by DeployWorkload tests directly.
func opsToFakeDockerAPI() dockerAPI {
	return &fakeDockerAPI{}
}

func TestDeployWorkload_Stdio_IsolatedNetwork_SkipsIngressAndSetsEgressEnv(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{
		dnsIP: "172.18.0.10",
	}
	fproxy := &fakeNetworkProxy{}
	c := newClientWithOpsAndProxy(fops, fproxy)

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

	// Proxy must be invoked for isolated-network deployment
	assert.True(t, fproxy.setupCalled)
	// stdio transport: no ingress needed
	assert.Equal(t, "stdio", fproxy.capturedSpec.TransportType)
	// AllowDockerGateway was not set
	assert.False(t, fproxy.capturedSpec.AllowDockerGateway)

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
}

func TestDeployWorkload_SSE_IsolatedNetwork_ReturnsIngressPortAndPassesDNS(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{
		dnsIP: "172.18.0.20",
	}
	fproxy := &fakeNetworkProxy{
		ingressPort: 18081,
	}
	c := newClientWithOpsAndProxy(fops, fproxy)

	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {
			{HostIP: "127.0.0.1", HostPort: ""},
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

	// For non-stdio with network isolation, returned port comes from ingress proxy.
	assert.Equal(t, 18081, hostPort)
	assert.True(t, fproxy.setupCalled)
	// The upstream port should be the first exposed port (8080).
	assert.Equal(t, 8080, fproxy.capturedSpec.UpstreamPort)
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

	// Should not create internal network, DNS, or invoke the proxy
	assert.False(t, fops.dnsCalled)
	assert.Empty(t, fops.createNetworkCalls, "internal network should not be created when isolation is disabled")

	// MCP should be created on default network (empty name)
	require.True(t, fops.mcpCalled)
	assert.Equal(t, "", fops.mcpNetworkName)

	// Returned host port should be the one from the binding (since auxiliary retains host port)
	assert.Equal(t, 56789, hostPort)
}

func TestDeployWorkload_AllowDockerGateway_ForwardedToEgress(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{dnsIP: "172.18.0.10"}
	fproxy := &fakeNetworkProxy{}
	c := newClientWithOpsAndProxy(fops, fproxy)

	opts := runtime.NewDeployWorkloadOptions()
	opts.AttachStdio = true
	opts.AllowDockerGateway = true

	_, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"app",
		[]string{"serve"},
		map[string]string{},
		map[string]string{},
		&permissions.Profile{},
		"stdio",
		opts,
		true, // isolateNetwork required for proxy to be invoked
	)
	require.NoError(t, err)

	require.True(t, fproxy.setupCalled, "proxy must be set up when isolateNetwork=true")
	assert.True(t, fproxy.capturedSpec.AllowDockerGateway, "AllowDockerGateway must be forwarded to SetupProxies")
}

// TestDeployWorkload_AllowDockerGateway_DefaultsToNotForwarded guards against a
// default flip: when the caller does not opt in, the egress proxy must keep its
// Docker-gateway deny rules. It also confirms the DNS container is spawned on the
// isolation path (the MCP server's resolver).
func TestDeployWorkload_AllowDockerGateway_DefaultsToNotForwarded(t *testing.T) {
	t.Parallel()

	fops := &fakeDeployOps{dnsIP: "172.18.0.10"}
	fproxy := &fakeNetworkProxy{}
	c := newClientWithOpsAndProxy(fops, fproxy)

	opts := runtime.NewDeployWorkloadOptions()
	opts.AttachStdio = true
	// AllowDockerGateway intentionally left at its zero value (false).

	_, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"app",
		[]string{"serve"},
		map[string]string{},
		map[string]string{},
		&permissions.Profile{},
		"stdio",
		opts,
		true, // isolateNetwork required for the proxy to be set up
	)
	require.NoError(t, err)

	require.True(t, fproxy.setupCalled, "network proxy must be set up when isolateNetwork=true")
	assert.False(t, fproxy.capturedSpec.AllowDockerGateway,
		"AllowDockerGateway must default to false so the gateway deny rules stay in place")
	assert.True(t, fops.dnsCalled, "DNS container must be created on the isolation path")
}

// TestDeployWorkload_NonBridgeNetwork_DropsIsolation verifies that when
// isolateNetwork=true but the network mode is non-bridge (host/none), the
// isolation sidecars are never created, no proxy env vars are injected, and the
// network-isolation label records the effective (false) value. Building the
// sidecars on the internal network for a host/none workload would silently break
// all outbound traffic. See issue #5775.
func TestDeployWorkload_NonBridgeNetwork_DropsIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		networkMode string
	}{
		{"host mode", "host"},
		{"none mode", "none"},
		{"custom mode", "container:foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fops := &fakeDeployOps{dnsIP: "172.18.0.10"}
			fproxy := &fakeNetworkProxy{ingressPort: 18081}
			c := newClientWithOpsAndProxy(fops, fproxy)

			opts := runtime.NewDeployWorkloadOptions()
			opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
			opts.PortBindings = map[string][]runtime.PortBinding{
				"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "12345"}},
			}

			labels := map[string]string{}
			env := map[string]string{"EXISTING": "1"}

			_, err := c.DeployWorkload(
				t.Context(),
				"ghcr.io/example/mcp:latest",
				"app",
				[]string{"serve"},
				env,
				labels,
				&permissions.Profile{
					Network: &permissions.NetworkPermissions{Mode: tt.networkMode},
				},
				"sse",
				opts,
				true, // isolateNetwork requested, but must be dropped for non-bridge modes
			)
			require.NoError(t, err)

			// No isolation sidecars should be created.
			assert.False(t, fops.dnsCalled, "DNS container must not be created for non-bridge modes")
			assert.False(t, fproxy.setupCalled, "network proxy must not be set up for non-bridge modes")
			assert.Empty(t, fops.createNetworkCalls, "internal network must not be created for non-bridge modes")

			// The external network is a bridge-only construct.
			assert.False(t, fops.externalNetworksCalled, "external network must not be created for non-bridge modes")

			// No proxy env vars should be injected.
			require.True(t, fops.mcpCalled)
			assert.NotContains(t, fops.mcpEnvVars, "HTTP_PROXY", "HTTP_PROXY must not be injected without isolation")
			assert.NotContains(t, fops.mcpEnvVars, "HTTPS_PROXY", "HTTPS_PROXY must not be injected without isolation")

			// createMcpContainer must receive the effective (false) isolation value.
			assert.False(t, fops.mcpIsolate, "createMcpContainer must receive effective isolation=false")

			// The network-isolation label must record the effective value.
			assert.False(t, lb.HasNetworkIsolation(labels),
				"network isolation label must record effective (false) value for non-bridge modes")
		})
	}
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

// TestDeployWorkload_Isolated_EgressBeforeMcpIngressAfter locks in the ordering
// contract: egress is provisioned BEFORE the MCP container (so proxy env vars are
// injected), and ingress is provisioned AFTER it. Creating the ingress before the
// MCP container caused the squid reverse proxy to cache a negative DNS lookup for
// the not-yet-existent upstream and never recover, leaving the workload stuck.
func TestDeployWorkload_Isolated_EgressBeforeMcpIngressAfter(t *testing.T) {
	t.Parallel()

	callOrder := make([]string, 0, 3)
	fops := &fakeDeployOps{
		dnsIP:     "172.18.0.10",
		callOrder: &callOrder,
	}
	fproxy := &fakeNetworkProxy{
		ingressPort: 18081,
		callOrder:   &callOrder,
	}
	c := newClientWithOpsAndProxy(fops, fproxy)

	opts := runtime.NewDeployWorkloadOptions()
	opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	opts.PortBindings = map[string][]runtime.PortBinding{
		"8080/tcp": {{HostIP: "127.0.0.1", HostPort: ""}},
	}

	_, err := c.DeployWorkload(
		t.Context(),
		"ghcr.io/example/mcp:latest",
		"app",
		[]string{"serve"},
		map[string]string{},
		map[string]string{},
		&permissions.Profile{},
		"sse",
		opts,
		true,
	)
	require.NoError(t, err)

	require.Equal(t, []string{"SetupEgress", "createMcpContainer", "SetupIngress"}, callOrder,
		"egress must be set up before the MCP container and ingress after it")
}
