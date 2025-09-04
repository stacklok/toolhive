package core

import (
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestSortWorkloadsByName(t *testing.T) {
	baseTime := time.Now()

	tests := []struct {
		name      string
		workloads []Workload
		want      []Workload
	}{
		{
			name: "sort alphabetically",
			workloads: []Workload{
				{
					Name:          "zebra",
					Package:       "package1",
					URL:           "http://localhost:8001",
					Port:          8001,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
				{
					Name:          "apple",
					Package:       "package2",
					URL:           "http://localhost:8002",
					Port:          8002,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
				{
					Name:          "mango",
					Package:       "package3",
					URL:           "http://localhost:8003",
					Port:          8003,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
			},
			want: []Workload{
				{
					Name:          "apple",
					Package:       "package2",
					URL:           "http://localhost:8002",
					Port:          8002,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
				{
					Name:          "mango",
					Package:       "package3",
					URL:           "http://localhost:8003",
					Port:          8003,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
				{
					Name:          "zebra",
					Package:       "package1",
					URL:           "http://localhost:8001",
					Port:          8001,
					ToolType:      "mcp",
					TransportType: types.TransportTypeSSE,
					Status:        runtime.WorkloadStatusRunning,
					CreatedAt:     baseTime,
				},
			},
		},
		{
			name: "already sorted",
			workloads: []Workload{
				{Name: "aaa"},
				{Name: "bbb"},
				{Name: "ccc"},
			},
			want: []Workload{
				{Name: "aaa"},
				{Name: "bbb"},
				{Name: "ccc"},
			},
		},
		{
			name: "reverse order",
			workloads: []Workload{
				{Name: "ccc"},
				{Name: "bbb"},
				{Name: "aaa"},
			},
			want: []Workload{
				{Name: "aaa"},
				{Name: "bbb"},
				{Name: "ccc"},
			},
		},
		{
			name: "with numbers",
			workloads: []Workload{
				{Name: "server-10"},
				{Name: "server-2"},
				{Name: "server-1"},
				{Name: "server-20"},
			},
			want: []Workload{
				{Name: "server-1"},
				{Name: "server-10"},
				{Name: "server-2"},
				{Name: "server-20"},
			},
		},
		{
			name:      "empty slice",
			workloads: []Workload{},
			want:      []Workload{},
		},
		{
			name: "single element",
			workloads: []Workload{
				{Name: "single"},
			},
			want: []Workload{
				{Name: "single"},
			},
		},
		{
			name: "case sensitive sorting",
			workloads: []Workload{
				{Name: "Zebra"},
				{Name: "apple"},
				{Name: "Banana"},
				{Name: "cherry"},
			},
			want: []Workload{
				{Name: "Banana"},
				{Name: "Zebra"},
				{Name: "apple"},
				{Name: "cherry"},
			},
		},
		{
			name: "with special characters",
			workloads: []Workload{
				{Name: "server_b"},
				{Name: "server-a"},
				{Name: "server.c"},
				{Name: "server@d"},
			},
			want: []Workload{
				{Name: "server-a"},
				{Name: "server.c"},
				{Name: "server@d"},
				{Name: "server_b"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying the test data
			workloadsCopy := make([]Workload, len(tt.workloads))
			copy(workloadsCopy, tt.workloads)

			SortWorkloadsByName(workloadsCopy)

			if !reflect.DeepEqual(workloadsCopy, tt.want) {
				t.Errorf("SortWorkloadsByName() got = %v, want %v", workloadsCopy, tt.want)
			}
		})
	}
}

func TestWorkload_Fields(t *testing.T) {
	now := time.Now()
	w := Workload{
		Name:          "test-workload",
		Package:       "test-package",
		URL:           "http://localhost:8080",
		Port:          8080,
		ToolType:      "mcp",
		TransportType: types.TransportTypeStdio,
		Status:        runtime.WorkloadStatusRunning,
		StatusContext: "healthy",
		CreatedAt:     now,
		Labels: map[string]string{
			"env": "test",
		},
		Group:       "test-group",
		ToolsFilter: []string{"tool1", "tool2"},
		Remote:      true,
	}

	if w.Name != "test-workload" {
		t.Errorf("Name = %v, want %v", w.Name, "test-workload")
	}
	if w.Package != "test-package" {
		t.Errorf("Package = %v, want %v", w.Package, "test-package")
	}
	if w.URL != "http://localhost:8080" {
		t.Errorf("URL = %v, want %v", w.URL, "http://localhost:8080")
	}
	if w.Port != 8080 {
		t.Errorf("Port = %v, want %v", w.Port, 8080)
	}
	if w.ToolType != "mcp" {
		t.Errorf("ToolType = %v, want %v", w.ToolType, "mcp")
	}
	if w.TransportType != types.TransportTypeStdio {
		t.Errorf("TransportType = %v, want %v", w.TransportType, types.TransportTypeStdio)
	}
	if w.Status != runtime.WorkloadStatusRunning {
		t.Errorf("Status = %v, want %v", w.Status, runtime.WorkloadStatusRunning)
	}
	if w.StatusContext != "healthy" {
		t.Errorf("StatusContext = %v, want %v", w.StatusContext, "healthy")
	}
	if !w.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", w.CreatedAt, now)
	}
	if len(w.Labels) != 1 || w.Labels["env"] != "test" {
		t.Errorf("Labels = %v, want %v", w.Labels, map[string]string{"env": "test"})
	}
	if w.Group != "test-group" {
		t.Errorf("Group = %v, want %v", w.Group, "test-group")
	}
	if len(w.ToolsFilter) != 2 || w.ToolsFilter[0] != "tool1" || w.ToolsFilter[1] != "tool2" {
		t.Errorf("ToolsFilter = %v, want %v", w.ToolsFilter, []string{"tool1", "tool2"})
	}
	if !w.Remote {
		t.Errorf("Remote = %v, want %v", w.Remote, true)
	}
}