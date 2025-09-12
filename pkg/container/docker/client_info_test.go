package docker

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestGetWorkloadInfo_MapsInspectResponseToDomain(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	createdStr := now.Format(time.RFC3339)

	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			call++
			if call == 1 {
				// First call: find by base name label -> return empty to force fallback
				return []container.Summary{}, nil
			}
			// Second call: exact name search -> return match
			return []container.Summary{
				{
					ID:     "cid-123",
					Names:  []string{"/mcp"},
					Labels: map[string]string{"toolhive": "true"},
					State:  "running",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-123", id)
			p8080, err := nat.NewPort("tcp", "8080")
			require.NoError(t, err)

			ns := &container.NetworkSettings{}
			ns.Ports = nat.PortMap{
				p8080: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "18080"}},
			}

			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:    "/mcp",
					Created: createdStr,
					State:   &container.State{Status: "running", Running: true},
				},
				Config: &container.Config{
					Image:  "ghcr.io/example/mcp:latest",
					Labels: map[string]string{"toolhive": "true", "k": "v"},
				},
				NetworkSettings: ns,
			}, nil
		},
	}

	c := &Client{api: api}

	info, err := c.GetWorkloadInfo(context.Background(), "mcp")
	require.NoError(t, err)

	assert.Equal(t, "mcp", info.Name)
	assert.Equal(t, "ghcr.io/example/mcp:latest", info.Image)
	assert.Equal(t, "running", info.Status)
	assert.Equal(t, rt.WorkloadStatusRunning, info.State)
	assert.WithinDuration(t, now, info.Created, time.Second)
	assert.Equal(t, map[string]string{"toolhive": "true", "k": "v"}, info.Labels)

	require.Len(t, info.Ports, 1)
	assert.Equal(t, rt.PortMapping{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"}, info.Ports[0])
}

func TestIsWorkloadRunning_TrueWhenDockerReportsRunning(t *testing.T) {
	t.Parallel()

	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			call++
			if call == 1 {
				// First call: base name lookup -> not found
				return []container.Summary{}, nil
			}
			// Second call: exact name lookup
			return []container.Summary{
				{
					ID:     "cid-xyz",
					Names:  []string{"/server"},
					Labels: map[string]string{"toolhive": "true"},
					State:  "running",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-xyz", id)

			ns := &container.NetworkSettings{}
			ns.Ports = nat.PortMap{}

			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:  "/server",
					State: &container.State{Status: "running", Running: true},
				},
				Config: &container.Config{
					Image: "img",
				},
				NetworkSettings: ns,
			}, nil
		},
	}

	c := &Client{api: api}

	ok, err := c.IsWorkloadRunning(context.Background(), "server")
	require.NoError(t, err)
	assert.True(t, ok)
}

// Additional coverage: port parse fallback and created time parse fallback
func TestGetWorkloadInfo_PortParseAndCreatedFallback(t *testing.T) {
	t.Parallel()

	call := 0
	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			call++
			if call == 1 {
				// First attempt (label-based) -> none found
				return []container.Summary{}, nil
			}
			// Second attempt (exact name) -> found one
			return []container.Summary{
				{
					ID:     "cid-badfields",
					Names:  []string{"/svc-bad"},
					Labels: map[string]string{"toolhive": "true"},
					State:  "exited",
				},
			}, nil
		},
		inspectFunc: func(_ context.Context, id string) (container.InspectResponse, error) {
			require.Equal(t, "cid-badfields", id)

			// Non-numeric host port; GetWorkloadInfo should log a warning and fall back to 0
			p8080, err := nat.NewPort("tcp", "8080")
			require.NoError(t, err)
			ns := &container.NetworkSettings{}
			ns.Ports = nat.PortMap{
				p8080: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "abc"}},
			}

			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:    "/svc-bad",
					State:   &container.State{Status: "exited", Running: false},
					Created: "not-a-time", // invalid RFC3339 -> Created should be zero time
				},
				Config:          &container.Config{Image: "img", Labels: map[string]string{"toolhive": "true"}},
				NetworkSettings: ns,
			}, nil
		},
	}

	c := &Client{api: api}

	info, err := c.GetWorkloadInfo(context.Background(), "svc-bad")
	require.NoError(t, err)

	// Created time should fall back to zero when parsing fails
	assert.True(t, info.Created.IsZero(), "expected zero time for invalid Created field")
	// State mapping for "exited" -> stopped
	assert.Equal(t, rt.WorkloadStatusStopped, info.State)

	// Port mapping should include container 8080/tcp with hostPort == 0 due to parse failure
	require.Len(t, info.Ports, 1)
	assert.Equal(t, 8080, info.Ports[0].ContainerPort)
	assert.Equal(t, 0, info.Ports[0].HostPort)
	assert.Equal(t, "tcp", info.Ports[0].Protocol)
}
