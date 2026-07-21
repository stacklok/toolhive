// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopWorkload_NotRunning_ReturnsNil(t *testing.T) {
	t.Parallel()

	// Arrange: find by exact name and inspect -> not running
	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ mobyclient.ContainerListOptions) ([]container.Summary, error) {
			call++
			if call == 1 {
				// First call: base-name label lookup -> none found
				return []container.Summary{}, nil
			}
			// Second call: exact name lookup -> found one
			return []container.Summary{
				{
					ID:     "cid-not-running",
					Names:  []string{"/svc"},
					Labels: map[string]string{"toolhive": "true"},
					State:  "exited",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-not-running", id)
			// Not running
			ns := &container.NetworkSettings{}
			ns.Ports = network.PortMap{}
			return container.InspectResponse{
				Name:                    "/svc",
				State:                   &container.State{Status: "exited", Running: false},
				Config:                  &container.Config{Image: "img", Labels: map[string]string{"toolhive": "true"}},
				NetworkSettings:         ns,
				ImageManifestDescriptor: nil,
			}, nil
		},
		// stopFunc should not be called
		stopFunc: func(_ context.Context, _ string, _ mobyclient.ContainerStopOptions) error {
			t.Fatalf("ContainerStop should not be called for not-running container")
			return nil
		},
	}
	c := &Client{api: api}

	// Act
	err := c.StopWorkload(t.Context(), "svc")

	// Assert
	require.NoError(t, err)
}

func TestStopWorkload_Running_CallsContainerStop(t *testing.T) {
	t.Parallel()

	called := false
	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ mobyclient.ContainerListOptions) ([]container.Summary, error) {
			call++
			if call == 1 {
				return []container.Summary{}, nil
			}
			return []container.Summary{
				{
					ID:     "cid-running",
					Names:  []string{"/app"},
					Labels: map[string]string{"toolhive": "true"}, // no network isolation -> avoids proxy stops
					State:  "running",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-running", id)
			ns := &container.NetworkSettings{}
			ns.Ports = network.PortMap{}
			return container.InspectResponse{
				Name:            "/app",
				State:           &container.State{Status: "running", Running: true},
				Config:          &container.Config{Image: "img", Labels: map[string]string{"toolhive": "true"}},
				NetworkSettings: ns,
			}, nil
		},
		stopFunc: func(_ context.Context, id string, _ mobyclient.ContainerStopOptions) error {
			// The implementation stops by workloadName (not ID), verify that
			assert.Equal(t, "app", id)
			called = true
			return nil
		},
	}
	c := &Client{api: api}

	err := c.StopWorkload(t.Context(), "app")
	require.NoError(t, err)
	assert.True(t, called, "expected ContainerStop to be called")
}

func TestStopWorkload_NotFound_ReturnsNil(t *testing.T) {
	t.Parallel()

	// Simulate a case where a container appears in listing, but inspect returns NotFound
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ mobyclient.ContainerListOptions) ([]container.Summary, error) {
			// Exact name lookup will find a candidate
			return []container.Summary{
				{
					ID:     "cid-missing",
					Names:  []string{"/gone"},
					Labels: map[string]string{"toolhive": "true"},
					State:  "exited",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-missing", id)
			// Return a NotFound error that satisfies errdefs.IsNotFound
			return container.InspectResponse{}, errdefs.ErrNotFound
		},
	}
	c := &Client{api: api}

	err := c.StopWorkload(t.Context(), "gone")
	// StopWorkload should treat a not-found workload as success
	require.NoError(t, err)
}

// TestStopProxyContainer_HandlesEnvoyAndSquidTopologies guards the cleanup path
// shared by both proxy backends. StopWorkload iterates the fixed proxy suffixes
// (-egress, -ingress, -dns) and calls stopProxyContainer for each. A legacy Squid
// workload has all three; an Envoy workload consolidates egress+ingress into one
// container and therefore has NO -ingress container. stopProxyContainer must stop
// a container that exists and silently tolerate one that does not, so the same
// teardown works for both the 2-container (Envoy) and 3-container (Squid)
// topologies without backend-specific logic. See #5902.
func TestStopProxyContainer_HandlesEnvoyAndSquidTopologies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		containerName string
		present       bool // whether the named proxy container exists
		wantStopped   bool
	}{
		{
			name:          "present container is stopped (Squid egress/ingress/dns, Envoy egress/dns)",
			containerName: "app-egress",
			present:       true,
			wantStopped:   true,
		},
		{
			name:          "absent ingress is tolerated (Envoy has no -ingress container)",
			containerName: "app-ingress",
			present:       false,
			wantStopped:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stoppedID string
			api := &fakeDockerAPI{
				listFunc: func(_ context.Context, _ mobyclient.ContainerListOptions) ([]container.Summary, error) {
					if !tt.present {
						return []container.Summary{}, nil
					}
					return []container.Summary{
						{ID: "cid-" + tt.containerName, Names: []string{"/" + tt.containerName}},
					}, nil
				},
				stopFunc: func(_ context.Context, id string, _ mobyclient.ContainerStopOptions) error {
					stoppedID = id
					return nil
				},
			}
			c := &Client{api: api}

			// Must not panic or error regardless of whether the container exists.
			c.stopProxyContainer(t.Context(), tt.containerName, 30)

			if tt.wantStopped {
				assert.Equal(t, "cid-"+tt.containerName, stoppedID,
					"expected the existing proxy container to be stopped")
			} else {
				assert.Empty(t, stoppedID,
					"expected no ContainerStop call for an absent proxy container")
			}
		})
	}
}
