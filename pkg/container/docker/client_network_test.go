// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
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
