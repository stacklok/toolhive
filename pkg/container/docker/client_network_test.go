// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// TestCreateNetwork_ConcurrentSameName reproduces the shared-network startup
// race: multiple workloads (e.g. several `thv run` sharing "toolhive-external")
// call createNetwork for the same name at once. The existence check and the
// create are not atomic, so without idempotent conflict handling all but one
// caller fails with "network ... already exists" and its workload never
// launches. This test drives that exact contention against a real daemon and
// asserts every caller succeeds with exactly one network created.
//
// Skips when Docker is unavailable, matching the real-daemon tests in this
// package (see envoy_test.go).
func TestCreateNetwork_ConcurrentSameName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mc, _, _, err := sdk.NewDockerClient(ctx)
	if err != nil {
		t.Skipf("docker not available; skipping network create race test: %v", err)
	}
	c := &Client{client: mc}

	// Unique per test process so parallel runs / leftovers never collide.
	name := fmt.Sprintf("toolhive-test-netrace-%d", os.Getpid())
	labels := map[string]string{"toolhive": "true"}
	t.Cleanup(func() { removeNetworksByName(context.Background(), mc, name) })
	removeNetworksByName(ctx, mc, name) // start clean

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximize contention
			errs[i] = c.createNetwork(ctx, name, labels, false)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		require.NoErrorf(t, e, "worker %d: concurrent createNetwork must treat a lost race as success", i)
	}

	require.Equal(t, 1, countNetworksByName(ctx, mc, name),
		"exactly one shared network should exist after the concurrent race")
}

func countNetworksByName(ctx context.Context, mc *mobyclient.Client, name string) int {
	networks, err := mc.NetworkList(ctx, mobyclient.NetworkListOptions{
		Filters: mobyclient.Filters{}.Add("name", name),
	})
	if err != nil {
		return -1
	}
	n := 0
	for _, item := range networks.Items {
		if item.Name == name {
			n++
		}
	}
	return n
}

func removeNetworksByName(ctx context.Context, mc *mobyclient.Client, name string) {
	networks, err := mc.NetworkList(ctx, mobyclient.NetworkListOptions{
		Filters: mobyclient.Filters{}.Add("name", name),
	})
	if err != nil {
		return
	}
	for _, item := range networks.Items {
		if item.Name == name {
			_, _ = mc.NetworkRemove(ctx, item.ID, mobyclient.NetworkRemoveOptions{})
		}
	}
}

type fakeBridgeNetworkAPI struct {
	list    func() (mobyclient.NetworkListResult, error)
	inspect func(string) (mobyclient.NetworkInspectResult, error)
}

func (f fakeBridgeNetworkAPI) NetworkList(_ context.Context, _ mobyclient.NetworkListOptions) (mobyclient.NetworkListResult, error) {
	return f.list()
}

func (f fakeBridgeNetworkAPI) NetworkInspect(_ context.Context, id string, _ mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
	return f.inspect(id)
}

func TestDiscoverBridgeGateways(t *testing.T) {
	t.Parallel()

	list := mobyclient.NetworkListResult{Items: []network.Summary{
		{Network: network.Network{ID: "bridge-b", Driver: "bridge"}},
		{Network: network.Network{ID: "overlay", Driver: "overlay"}},
		{Network: network.Network{ID: "bridge-a", Driver: "bridge"}},
	}}
	inspects := map[string]mobyclient.NetworkInspectResult{
		"bridge-a": {Network: network.Inspect{Network: network.Network{ID: "bridge-a", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{
			{Gateway: netip.MustParseAddr("2001:db8::1")},
			{Gateway: netip.MustParseAddr("192.168.5.1")},
		}}}}},
		"bridge-b": {Network: network.Inspect{Network: network.Network{ID: "bridge-b", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{
			{Gateway: netip.MustParseAddr("192.168.5.1")},
		}}}}},
	}
	api := fakeBridgeNetworkAPI{
		list: func() (mobyclient.NetworkListResult, error) { return list, nil },
		inspect: func(id string) (mobyclient.NetworkInspectResult, error) {
			return inspects[id], nil
		},
	}

	gateways, err := (&Client{bridgeAPI: api}).discoverBridgeGateways(t.Context())
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{
		netip.MustParseAddr("192.168.5.1"),
		netip.MustParseAddr("2001:db8::1"),
	}, gateways)
}

func TestDiscoverBridgeGatewaysPodmanSkipsGatewaylessNetworks(t *testing.T) {
	t.Parallel()

	list := mobyclient.NetworkListResult{Items: []network.Summary{
		{Network: network.Network{ID: "gatewayless", Driver: "bridge"}},
		{Network: network.Network{ID: "routable", Driver: "bridge"}},
	}}
	inspects := map[string]mobyclient.NetworkInspectResult{
		"gatewayless": {Network: network.Inspect{Network: network.Network{
			ID: "gatewayless", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{}}},
		}}},
		"routable": {Network: network.Inspect{Network: network.Network{
			ID: "routable", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{
				Gateway: netip.MustParseAddr("10.89.2.1"),
			}}},
		}}},
	}
	api := fakeBridgeNetworkAPI{
		list: func() (mobyclient.NetworkListResult, error) { return list, nil },
		inspect: func(id string) (mobyclient.NetworkInspectResult, error) {
			return inspects[id], nil
		},
	}

	gateways, err := (&Client{runtimeType: runtime.TypePodman, bridgeAPI: api}).discoverBridgeGateways(t.Context())
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("10.89.2.1")}, gateways)
}

func TestDiscoverBridgeGatewaysFailsClosed(t *testing.T) {
	t.Parallel()

	bridgeList := mobyclient.NetworkListResult{Items: []network.Summary{{
		Network: network.Network{ID: "bridge-id", Driver: "bridge"},
	}}}
	tests := []struct {
		name      string
		api       fakeBridgeNetworkAPI
		runtime   runtime.Type
		wantError string
	}{
		{
			name:      "list error",
			wantError: "list Docker networks",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) {
					return mobyclient.NetworkListResult{}, errors.New("list")
				},
			},
		},
		{
			name:      "no bridge networks",
			wantError: "no bridge gateways",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) { return mobyclient.NetworkListResult{}, nil },
			},
		},
		{
			name:      "inspect error",
			wantError: "inspect Docker bridge network",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) { return bridgeList, nil },
				inspect: func(string) (mobyclient.NetworkInspectResult, error) {
					return mobyclient.NetworkInspectResult{}, errors.New("inspect")
				},
			},
		},
		{
			name:      "empty IPAM",
			runtime:   runtime.TypeDocker,
			wantError: "has no IPAM configuration",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) { return bridgeList, nil },
				inspect: func(string) (mobyclient.NetworkInspectResult, error) {
					return mobyclient.NetworkInspectResult{Network: network.Inspect{Network: network.Network{
						ID: "bridge-id", Driver: "bridge",
					}}}, nil
				},
			},
		},
		{
			name:      "Docker rejects absent gateway",
			runtime:   runtime.TypeDocker,
			wantError: "invalid IPAM gateway",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) { return bridgeList, nil },
				inspect: func(string) (mobyclient.NetworkInspectResult, error) {
					return mobyclient.NetworkInspectResult{Network: network.Inspect{Network: network.Network{
						ID: "bridge-id", Driver: "bridge",
						IPAM: network.IPAM{Config: []network.IPAMConfig{{}}},
					}}}, nil
				},
			},
		},
		{
			name:      "Podman with only gatewayless networks",
			runtime:   runtime.TypePodman,
			wantError: "no bridge gateways",
			api: fakeBridgeNetworkAPI{
				list: func() (mobyclient.NetworkListResult, error) { return bridgeList, nil },
				inspect: func(string) (mobyclient.NetworkInspectResult, error) {
					return mobyclient.NetworkInspectResult{Network: network.Inspect{Network: network.Network{
						ID: "bridge-id", Driver: "bridge",
						IPAM: network.IPAM{Config: []network.IPAMConfig{{}}},
					}}}, nil
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gateways, err := (&Client{runtimeType: tt.runtime, bridgeAPI: tt.api}).discoverBridgeGateways(t.Context())
			require.ErrorContains(t, err, tt.wantError)
			require.Empty(t, gateways)
		})
	}
}

func TestDiscoverBridgeGatewaysRelistsOnceOnConcurrentRemoval(t *testing.T) {
	t.Parallel()

	lists := 0
	api := fakeBridgeNetworkAPI{
		list: func() (mobyclient.NetworkListResult, error) {
			lists++
			return mobyclient.NetworkListResult{Items: []network.Summary{{
				Network: network.Network{ID: fmt.Sprintf("bridge-%d", lists), Driver: "bridge"},
			}}}, nil
		},
		inspect: func(id string) (mobyclient.NetworkInspectResult, error) {
			if id == "bridge-1" {
				return mobyclient.NetworkInspectResult{}, errdefs.ErrNotFound
			}
			return mobyclient.NetworkInspectResult{Network: network.Inspect{Network: network.Network{
				ID: id, Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{Gateway: netip.MustParseAddr("10.0.0.1")}}},
			}}}, nil
		},
	}

	gateways, err := (&Client{bridgeAPI: api}).discoverBridgeGateways(t.Context())
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("10.0.0.1")}, gateways)
	require.Equal(t, 2, lists)
}

func TestDiscoverBridgeGatewaysHasNoFallback(t *testing.T) {
	t.Parallel()

	gateways, err := (&Client{}).discoverBridgeGateways(t.Context())
	require.Error(t, err)
	require.Nil(t, gateways)
}
