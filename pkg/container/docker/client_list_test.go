package docker

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

// fakeDockerAPI provides a minimal test double for dockerAPI used by Client.
type fakeDockerAPI struct {
	listFunc    func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	inspectFunc func(ctx context.Context, id string) (container.InspectResponse, error)
}

func (f *fakeDockerAPI) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if f.listFunc != nil {
		return f.listFunc(ctx, options)
	}
	return nil, nil
}

func (f *fakeDockerAPI) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if f.inspectFunc != nil {
		return f.inspectFunc(ctx, id)
	}
	return container.InspectResponse{}, nil
}

func TestListWorkloads_FiltersAuxiliaryAndMapsFields(t *testing.T) {
	t.Parallel()

	created := time.Now().Add(-1 * time.Hour).Unix()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{
					ID:      "aux1",
					Image:   "aux:image",
					Status:  "Up 10 minutes",
					State:   "running",
					Names:   []string{"/aux-name"},
					Labels:  map[string]string{ToolhiveAuxiliaryWorkloadLabel: LabelValueTrue, "toolhive": "true"},
					Ports:   []container.Port{{PrivatePort: 3128, PublicPort: 0, Type: "tcp"}},
					Created: created,
				},
				{
					ID:      "cid1",
					Image:   "srv:image",
					Status:  "Up 1 minute",
					State:   "running",
					Names:   []string{"/mcp-name"},
					Labels:  map[string]string{"toolhive": "true", "custom": "x"},
					Ports:   []container.Port{{PrivatePort: 8080, PublicPort: 18080, Type: "tcp"}},
					Created: created,
				},
			}, nil
		},
	}

	c := &Client{api: api}

	ctx := context.Background()
	items, err := c.ListWorkloads(ctx)
	require.NoError(t, err)

	// Auxiliary container should be filtered out
	require.Len(t, items, 1)

	got := items[0]
	assert.Equal(t, "mcp-name", got.Name)
	assert.Equal(t, "srv:image", got.Image)
	assert.Equal(t, "Up 1 minute", got.Status)
	assert.Equal(t, rt.WorkloadStatusRunning, got.State) // via dockerToDomainStatus("running")
	assert.WithinDuration(t, time.Unix(created, 0), got.Created, time.Second)
	assert.Equal(t, map[string]string{"toolhive": "true", "custom": "x"}, got.Labels)

	require.Len(t, got.Ports, 1)
	assert.Equal(t, rt.PortMapping{ContainerPort: 8080, HostPort: 18080, Protocol: "tcp"}, got.Ports[0])
}
