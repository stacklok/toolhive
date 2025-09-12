package docker

import (
	"context"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopWorkload_NotRunning_ReturnsNil(t *testing.T) {
	t.Parallel()

	// Arrange: find by exact name and inspect -> not running
	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
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
			ns.Ports = nat.PortMap{}
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:  "/svc",
					State: &container.State{Status: "exited", Running: false},
				},
				Config:                  &container.Config{Image: "img", Labels: map[string]string{"toolhive": "true"}},
				NetworkSettings:         ns,
				ImageManifestDescriptor: nil,
			}, nil
		},
		// stopFunc should not be called
		stopFunc: func(_ context.Context, _ string, _ container.StopOptions) error {
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
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
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
			ns.Ports = nat.PortMap{}
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:  "/app",
					State: &container.State{Status: "running", Running: true},
				},
				Config:          &container.Config{Image: "img", Labels: map[string]string{"toolhive": "true"}},
				NetworkSettings: ns,
			}, nil
		},
		stopFunc: func(_ context.Context, id string, _ container.StopOptions) error {
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
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
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
