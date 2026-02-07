// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"golang.org/x/sync/errgroup"

	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/core"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
)

func TestGetWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		workloadName   string
		setupMock      func(*workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "workload not found",
		},
		{
			name:         "invalid workload name",
			workloadName: "invalid-name",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "invalid-name").
					Return(core.Workload{}, wt.ErrInvalidWorkloadName)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid workload name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(mockWorkloadManager, mockRuntime, mockGroupManager)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
			}

			req := httptest.NewRequest("GET", "/"+tt.workloadName, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.getWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestCreateWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		requestBody    string
		setupMock      func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:        "invalid JSON",
			requestBody: `{"name":`,
			setupMock: func(_ *testing.T, _ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "failed to decode request",
		},
		{
			name:        "workload already exists",
			requestBody: `{"name": "existing-workload", "image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "existing-workload").Return(true, nil)
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "workload with name existing-workload already exists",
		},
		{
			name:        "invalid proxy mode",
			requestBody: `{"name": "test-workload", "image": "test-image", "proxy_mode": "invalid"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil).AnyTimes()
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid proxy_mode",
		},
		{
			name:        "with tool filters",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools": ["filter1", "filter2"]}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1", "filter2"}

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string(nil)

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with both tool filters and tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools": ["filter1"], "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1"}

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with bogus tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "", "description": ""}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "tool override for actual-tool must have either Name or Description set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)

			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)

			mockRetriever := makeMockRetriever(t,
				"test-image",
				"test-image",
				&regtypes.ImageMetadata{Image: "test-image"},
				nil,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:    mockGroupManager,
					workloadManager: mockWorkloadManager,
					imageRetriever:  mockRetriever,
					appConfig:       &config.Config{},
				},
			}

			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.createWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		workloadName   string
		requestBody    string
		setupMock      func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:         "invalid JSON",
			workloadName: "test-workload",
			requestBody:  `{"image":`,
			setupMock: func(_ *testing.T, _ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid JSON",
		},
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "workload not found",
		},
		{
			name:         "stop workload fails",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					Return(nil, fmt.Errorf("stop failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error", // 5xx errors return generic message
		},
		{
			name:         "delete workload fails",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					Return(nil, fmt.Errorf("delete failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error", // 5xx errors return generic message
		},
		{
			name:         "with tool filters",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools": ["filter1", "filter2"]}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1", "filter2"}
				toolsOverride := map[string]runner.ToolOverride{}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string(nil)
				toolsOverride := map[string]runner.ToolOverride{
					"actual-tool": {
						Name:        "override-tool",
						Description: "Overridden tool",
					},
				}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with both tool filters and tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools": ["filter1"], "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1"}
				toolsOverride := map[string]runner.ToolOverride{
					"actual-tool": {
						Name:        "override-tool",
						Description: "Overridden tool",
					},
				}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with bogus tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "", "description": ""}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				// The validation error should occur before UpdateWorkload is called
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "tool override for actual-tool must have either Name or Description set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)

			mockRetriever := makeMockRetriever(t,
				"test-image",
				"test-image",
				&regtypes.ImageMetadata{Image: "test-image"},
				nil,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:    mockGroupManager,
					workloadManager: mockWorkloadManager,
					imageRetriever:  mockRetriever,
					appConfig:       &config.Config{},
				},
			}

			req := httptest.NewRequest("POST", "/"+tt.workloadName+"/edit", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

// TestUpdateWorkload_PortReuse tests the port reuse logic when editing workloads
func TestUpdateWorkload_PortReuse(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	tests := []struct {
		name           string
		workloadName   string
		requestBody    string
		existingPort   int
		setupMock      func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
		description    string
	}{
		{
			name:         "Edit with port=0 should reuse existing port",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image", "proxy_port": 0}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should be reused from existing workload")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When proxy_port is 0, the existing port should be reused",
		},
		{
			name:         "Edit with explicit port should use that port",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image", "proxy_port": 9090}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, 9090, runConfig.Port, "Port should be set to explicitly requested port")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When an explicit port is provided, it should be used instead of reusing",
		},
		{
			name:         "Edit with same port should skip validation",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image", "proxy_port": 8080}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should remain the same")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When reusing the same port, validation should be skipped",
		},
		{
			name:         "Edit with no port specified should default to existing",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (*errgroup.Group, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should default to existing port")
						return &errgroup.Group{}, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When no port is specified in request, existing port should be reused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)

			mockRetriever := makeMockRetriever(t,
				"test-image",
				"test-image",
				&regtypes.ImageMetadata{Image: "test-image"},
				nil,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:     mockGroupManager,
					workloadManager:  mockWorkloadManager,
					containerRuntime: mockRuntime,
					imageRetriever:   mockRetriever,
					appConfig:        &config.Config{},
				},
			}

			req := httptest.NewRequest("POST", "/api/v1beta/workloads/"+tt.workloadName+"/edit",
				strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)
			assert.Contains(t, w.Body.String(), tt.expectedBody, tt.description)
		})
	}
}

func makeMockRetriever(
	t *testing.T,
	expectedServerOrImage string,
	returnedImage string,
	returnedServerMetadata regtypes.ServerMetadata,
	returnedError error,
) retriever.Retriever {
	t.Helper()

	return func(_ context.Context, serverOrImage string, _ string, verificationType string, _ string, _ *templates.RuntimeConfig) (string, regtypes.ServerMetadata, error) {
		assert.Equal(t, expectedServerOrImage, serverOrImage)
		assert.Equal(t, retriever.VerifyImageWarn, verificationType)
		return returnedImage, returnedServerMetadata, returnedError
	}
}
