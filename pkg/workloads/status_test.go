package workloads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
)

const testWorkloadName = "test-workload"

func init() {
	logger.Initialize()
}

func TestNewStatusManagerFromRuntime(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := NewStatusManagerFromRuntime(mockRuntime)

	assert.NotNil(t, manager)
	assert.IsType(t, &runtimeStatusManager{}, manager)

	rsm := manager.(*runtimeStatusManager)
	assert.Equal(t, mockRuntime, rsm.runtime)
}

func TestRuntimeStatusManager_CreateWorkloadStatus(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &runtimeStatusManager{runtime: mockRuntime}

	ctx := context.Background()

	err := manager.CreateWorkloadStatus(ctx, testWorkloadName)
	assert.NoError(t, err)
}

func TestRuntimeStatusManager_GetWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadName  string
		setupMock     func(*mocks.MockRuntime)
		expectedError string
		expectedName  string
	}{
		{
			name:         "successful get workload",
			workloadName: "test-workload",
			setupMock: func(m *mocks.MockRuntime) {
				info := rt.ContainerInfo{
					Name:    "test-workload",
					Image:   "test-image:latest",
					Status:  "running",
					State:   rt.WorkloadStatusRunning,
					Created: time.Now(),
					Labels: map[string]string{
						"toolhive":           "true",
						"toolhive-name":      "test-workload",
						"toolhive-transport": "sse",
						"toolhive-port":      "8080",
						"toolhive-tool-type": "mcp",
					},
				}
				m.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(info, nil)
			},
			expectedName: "test-workload",
		},
		{
			name:          "invalid workload name",
			workloadName:  "",
			setupMock:     func(_ *mocks.MockRuntime) {},
			expectedError: "workload name cannot be empty",
		},
		{
			name:         "runtime error",
			workloadName: "test-workload",
			setupMock: func(m *mocks.MockRuntime) {
				m.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(rt.ContainerInfo{}, errors.New("runtime error"))
			},
			expectedError: "runtime error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := mocks.NewMockRuntime(ctrl)
			tt.setupMock(mockRuntime)

			manager := &runtimeStatusManager{runtime: mockRuntime}
			ctx := context.Background()

			workload, err := manager.GetWorkload(ctx, tt.workloadName)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Empty(t, workload.Name)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedName, workload.Name)
			}
		})
	}
}

func TestRuntimeStatusManager_ListWorkloads(t *testing.T) {
	t.Parallel()

	now := time.Now()
	runningContainer := rt.ContainerInfo{
		Name:    "running-workload",
		Image:   "test-image:latest",
		Status:  "Up 5 minutes",
		State:   rt.WorkloadStatusRunning,
		Created: now,
		Labels: map[string]string{
			"toolhive":           "true",
			"toolhive-name":      "running-workload",
			"toolhive-transport": "sse",
			"toolhive-port":      "8080",
			"toolhive-tool-type": "mcp",
			"custom-label":       "value1",
		},
	}

	stoppedContainer := rt.ContainerInfo{
		Name:    "stopped-workload",
		Image:   "test-image:latest",
		Status:  "Exited (0) 2 minutes ago",
		State:   rt.WorkloadStatusStopped,
		Created: now.Add(-time.Hour),
		Labels: map[string]string{
			"toolhive":           "true",
			"toolhive-name":      "stopped-workload",
			"toolhive-transport": "http",
			"toolhive-port":      "8081",
			"toolhive-tool-type": "mcp",
			"environment":        "test",
		},
	}

	tests := []struct {
		name           string
		listAll        bool
		labelFilters   []string
		setupMock      func(*mocks.MockRuntime)
		expectedCount  int
		expectedError  string
		checkWorkloads func([]Workload)
	}{
		{
			name:    "list running workloads only",
			listAll: false,
			setupMock: func(m *mocks.MockRuntime) {
				containers := []rt.ContainerInfo{runningContainer, stoppedContainer}
				m.EXPECT().ListWorkloads(gomock.Any()).Return(containers, nil)
			},
			expectedCount: 1,
			checkWorkloads: func(workloads []Workload) {
				assert.Equal(t, "running-workload", workloads[0].Name)
				assert.Equal(t, rt.WorkloadStatusRunning, workloads[0].Status)
			},
		},
		{
			name:    "list all workloads",
			listAll: true,
			setupMock: func(m *mocks.MockRuntime) {
				containers := []rt.ContainerInfo{runningContainer, stoppedContainer}
				m.EXPECT().ListWorkloads(gomock.Any()).Return(containers, nil)
			},
			expectedCount: 2,
		},
		{
			name:         "list with label filter",
			listAll:      true,
			labelFilters: []string{"environment=test"},
			setupMock: func(m *mocks.MockRuntime) {
				containers := []rt.ContainerInfo{runningContainer, stoppedContainer}
				m.EXPECT().ListWorkloads(gomock.Any()).Return(containers, nil)
			},
			expectedCount: 1,
			checkWorkloads: func(workloads []Workload) {
				assert.Equal(t, "stopped-workload", workloads[0].Name)
			},
		},
		{
			name:         "invalid label filter",
			listAll:      true,
			labelFilters: []string{"invalid-filter"},
			setupMock: func(m *mocks.MockRuntime) {
				// Runtime is called before label parsing, so we need to mock it
				containers := []rt.ContainerInfo{runningContainer}
				m.EXPECT().ListWorkloads(gomock.Any()).Return(containers, nil)
			},
			expectedError: "failed to parse label filters",
		},
		{
			name:    "runtime error",
			listAll: true,
			setupMock: func(m *mocks.MockRuntime) {
				m.EXPECT().ListWorkloads(gomock.Any()).Return(nil, errors.New("runtime error"))
			},
			expectedError: "failed to list containers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := mocks.NewMockRuntime(ctrl)
			tt.setupMock(mockRuntime)

			manager := &runtimeStatusManager{runtime: mockRuntime}
			ctx := context.Background()

			workloads, err := manager.ListWorkloads(ctx, tt.listAll, tt.labelFilters)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, workloads)
			} else {
				assert.NoError(t, err)
				assert.Len(t, workloads, tt.expectedCount)
				if tt.checkWorkloads != nil {
					tt.checkWorkloads(workloads)
				}
			}
		})
	}
}

func TestRuntimeStatusManager_SetWorkloadStatus(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &runtimeStatusManager{runtime: mockRuntime}

	ctx := context.Background()
	status := rt.WorkloadStatusRunning
	contextMsg := "test context"

	manager.SetWorkloadStatus(ctx, testWorkloadName, status, contextMsg)
}

func TestRuntimeStatusManager_DeleteWorkloadStatus(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &runtimeStatusManager{runtime: mockRuntime}

	ctx := context.Background()

	err := manager.DeleteWorkloadStatus(ctx, testWorkloadName)
	assert.NoError(t, err)
}

func TestParseLabelFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		labelFilters   []string
		expectedResult map[string]string
		expectedError  string
	}{
		{
			name:           "empty filters",
			labelFilters:   []string{},
			expectedResult: map[string]string{},
		},
		{
			name:         "single valid filter",
			labelFilters: []string{"key=value"},
			expectedResult: map[string]string{
				"key": "value",
			},
		},
		{
			name:         "multiple valid filters",
			labelFilters: []string{"env=prod", "version=1.0"},
			expectedResult: map[string]string{
				"env":     "prod",
				"version": "1.0",
			},
		},
		{
			name:          "invalid filter format",
			labelFilters:  []string{"invalid-filter"},
			expectedError: "invalid label filter 'invalid-filter'",
		},
		{
			name:          "mixed valid and invalid filters",
			labelFilters:  []string{"env=prod", "invalid"},
			expectedError: "invalid label filter 'invalid'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := parseLabelFilters(tt.labelFilters)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestMatchesLabelFilters(t *testing.T) {
	t.Parallel()

	workloadLabels := map[string]string{
		"env":     "prod",
		"version": "1.0",
		"team":    "platform",
	}

	tests := []struct {
		name     string
		filters  map[string]string
		expected bool
	}{
		{
			name:     "empty filters",
			filters:  map[string]string{},
			expected: true,
		},
		{
			name: "single matching filter",
			filters: map[string]string{
				"env": "prod",
			},
			expected: true,
		},
		{
			name: "multiple matching filters",
			filters: map[string]string{
				"env":     "prod",
				"version": "1.0",
			},
			expected: true,
		},
		{
			name: "single non-matching filter",
			filters: map[string]string{
				"env": "dev",
			},
			expected: false,
		},
		{
			name: "missing label in workload",
			filters: map[string]string{
				"missing": "value",
			},
			expected: false,
		},
		{
			name: "mixed matching and non-matching",
			filters: map[string]string{
				"env":     "prod",
				"version": "2.0",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := matchesLabelFilters(workloadLabels, tt.filters)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateWorkloadName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		testWorkloadName string
		expectError      bool
	}{
		{
			name:             "valid workload name",
			testWorkloadName: "test-workload",
			expectError:      false,
		},
		{
			name:             "empty workload name",
			testWorkloadName: "",
			expectError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateWorkloadName(tt.testWorkloadName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "workload name cannot be empty")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
