package docker

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestGetWorkloadStats_ContainerFound(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{
				ID:    "cid-123",
				Names: []string{"/test-workload"},
			}}, nil
		},
		statsFunc: func(_ context.Context, id string, _ bool) (container.StatsResponseReader, error) {
			require.Equal(t, "cid-123", id)

			stats := `{
				"read": "2024-06-01T12:00:00.000000000Z",
				"precpu_stats": {
					"cpu_usage": {
						"total_usage": 1000000000
					},
					"system_cpu_usage": 200000000000
				},
				"cpu_stats": {
					"cpu_usage": {
						"total_usage": 1500000000
					},
					"system_cpu_usage": 210000000000,
					"online_cpus": 1
				},
				"memory_stats": {
					"usage": 52428800,
					"limit": 209715200
				}
			}`

			return container.StatsResponseReader{
				Body: io.NopCloser(strings.NewReader(stats)),
			}, nil
		},
	}

	c := &Client{api: api}

	stats, err := c.GetWorkloadStats(context.Background(), "test-workload")
	require.NoError(t, err)

	assert.InDelta(t, 5.0, stats.CPUPercent, 0.001)
	assert.Equal(t, uint64(52428800), stats.MemoryUsage)
	assert.Equal(t, uint64(209715200), stats.MemoryLimit)
}

func TestGetWorkloadStats_ContainerNotFound(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{}, nil
		},
	}

	c := &Client{api: api}

	_, err := c.GetWorkloadStats(context.Background(), "nonexistent-workload")
	require.Error(t, err)
	assert.Equal(t, runtime.ErrWorkloadNotFound, err)
}

func TestGetWorkloadStats_ZeroCPUDelta(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{
				ID:    "cid-123",
				Names: []string{"/test-workload"},
			}}, nil
		},
		statsFunc: func(_ context.Context, _ string, _ bool) (container.StatsResponseReader, error) {
			// Same CPU usage in both readings = 0% CPU
			stats := `{
				"precpu_stats": {
					"cpu_usage": {"total_usage": 1000000000},
					"system_cpu_usage": 200000000000
				},
				"cpu_stats": {
					"cpu_usage": {"total_usage": 1000000000},
					"system_cpu_usage": 210000000000
				},
				"memory_stats": {"usage": 1024, "limit": 2048}
			}`
			return container.StatsResponseReader{
				Body: io.NopCloser(strings.NewReader(stats)),
			}, nil
		},
	}

	c := &Client{api: api}
	stats, err := c.GetWorkloadStats(context.Background(), "test-workload")
	require.NoError(t, err)

	assert.Equal(t, float64(0), stats.CPUPercent)
}

func TestGetWorkloadStats_ZeroSystemDelta(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{
				ID:    "cid-123",
				Names: []string{"/test-workload"},
			}}, nil
		},
		statsFunc: func(_ context.Context, _ string, _ bool) (container.StatsResponseReader, error) {
			// Same system CPU = avoid division by zero, should return 0%
			stats := `{
				"precpu_stats": {
					"cpu_usage": {"total_usage": 1000000000},
					"system_cpu_usage": 200000000000
				},
				"cpu_stats": {
					"cpu_usage": {"total_usage": 1500000000},
					"system_cpu_usage": 200000000000
				},
				"memory_stats": {"usage": 1024, "limit": 2048}
			}`
			return container.StatsResponseReader{
				Body: io.NopCloser(strings.NewReader(stats)),
			}, nil
		},
	}

	c := &Client{api: api}
	stats, err := c.GetWorkloadStats(context.Background(), "test-workload")
	require.NoError(t, err)

	// Should be 0 to avoid division by zero
	assert.Equal(t, float64(0), stats.CPUPercent)
}

func TestGetWorkloadStats_ZeroMemory(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{
				ID:    "cid-123",
				Names: []string{"/test-workload"},
			}}, nil
		},
		statsFunc: func(_ context.Context, _ string, _ bool) (container.StatsResponseReader, error) {
			stats := `{
				"precpu_stats": {
					"cpu_usage": {"total_usage": 0},
					"system_cpu_usage": 0
				},
				"cpu_stats": {
					"cpu_usage": {"total_usage": 0},
					"system_cpu_usage": 0
				},
				"memory_stats": {"usage": 0, "limit": 0}
			}`
			return container.StatsResponseReader{
				Body: io.NopCloser(strings.NewReader(stats)),
			}, nil
		},
	}

	c := &Client{api: api}
	stats, err := c.GetWorkloadStats(context.Background(), "test-workload")
	require.NoError(t, err)

	assert.Equal(t, uint64(0), stats.MemoryUsage)
	assert.Equal(t, uint64(0), stats.MemoryLimit)
	assert.Equal(t, float64(0), stats.CPUPercent)
}

func TestGetWorkloadStats_MultipleCPUs(t *testing.T) {
	t.Parallel()

	api := &fakeDockerAPI{
		listFunc: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{{
				ID:    "cid-123",
				Names: []string{"/test-workload"},
			}}, nil
		},
		statsFunc: func(_ context.Context, _ string, _ bool) (container.StatsResponseReader, error) {
			// With 4 CPUs, 50% of system delta used = 200% total CPU
			stats := `{
				"precpu_stats": {
					"cpu_usage": {"total_usage": 1000000000},
					"system_cpu_usage": 200000000000
				},
				"cpu_stats": {
					"cpu_usage": {"total_usage": 6000000000},
					"system_cpu_usage": 210000000000,
					"online_cpus": 4
				},
				"memory_stats": {"usage": 1024, "limit": 2048}
			}`
			return container.StatsResponseReader{
				Body: io.NopCloser(strings.NewReader(stats)),
			}, nil
		},
	}

	c := &Client{api: api}
	stats, err := c.GetWorkloadStats(context.Background(), "test-workload")
	require.NoError(t, err)

	// (5000000000 / 10000000000) * 4 * 100 = 200%
	assert.InDelta(t, 200.0, stats.CPUPercent, 0.001)
}
